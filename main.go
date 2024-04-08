package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/docker/docker/libnetwork/types"
	"github.com/olljanat/docker-bgp-lb/api"
	"github.com/sirupsen/logrus"
)

var scs = spew.ConfigState{Indent: "  "}

type bgpEndpoint struct {
	macAddress  net.HardwareAddr
	vethInside  string
	vethOutside string
}

type bgpNetwork struct {
	bridgeName string
	endpoints  map[string]*bgpEndpoint
}

type BgpLB struct {
	scope    string
	networks map[string]*bgpNetwork
	sync.Mutex
}

func (d *BgpLB) GetIpamCapabilities() (*api.CapabilitiesResponse, error) {
	return &api.CapabilitiesResponse{RequiresMACAddress: true}, nil
}

func (d *BgpLB) GetNetCapabilities() (*api.CapabilitiesResponse, error) {
	capabilities := &api.CapabilitiesResponse{
		Scope: d.scope,
	}

	return capabilities, nil
}

func (d *BgpLB) GetDefaultAddressSpaces() (*api.AddressSpacesResponse, error) {
	return &api.AddressSpacesResponse{LocalDefaultAddressSpace: api.LocalScope,
		GlobalDefaultAddressSpace: api.GlobalScope}, nil
}

func (d *BgpLB) RequestPool(r *api.RequestPoolRequest) (*api.RequestPoolResponse, error) {
	if r.Pool == "" {
		return &api.RequestPoolResponse{}, errors.New("Subnet is required")
	}

	return &api.RequestPoolResponse{PoolID: r.Pool, Pool: r.Pool}, nil
}

func (d *BgpLB) RequestAddress(r *api.RequestAddressRequest) (*api.RequestAddressResponse, error) {
	if r.Address == "" {
		return &api.RequestAddressResponse{}, errors.New("IP is required")
	}

	_, ipnet, err := net.ParseCIDR(r.PoolID)
	if err != nil {
		return &api.RequestAddressResponse{}, err
	}
	mask, _ := ipnet.Mask.Size()
	addr := fmt.Sprintf("%s/%s", r.Address, strconv.Itoa(mask))
	return &api.RequestAddressResponse{Address: addr}, nil
}

func (d *BgpLB) ReleaseAddress(r *api.ReleaseAddressRequest) error {
	return nil
}

func (d *BgpLB) ReleasePool(r *api.ReleasePoolRequest) error {
	return nil
}

func (d *BgpLB) CreateNetwork(r *api.CreateNetworkRequest) error {

	d.Lock()
	defer d.Unlock()

	if _, ok := d.networks[r.NetworkID]; ok {
		return types.ForbiddenErrorf("network %s exists", r.NetworkID)
	}

	bridgeName, err := createBridge(r.NetworkID, r.IPv4Data[0].Gateway)
	if err != nil {
		return err
	}

	bgpNetwork := &bgpNetwork{
		bridgeName: bridgeName,
		endpoints:  make(map[string]*bgpEndpoint),
	}

	d.networks[r.NetworkID] = bgpNetwork

	return nil
}

func (d *BgpLB) DeleteNetwork(r *api.DeleteNetworkRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Skip if not in map */
	if _, ok := d.networks[r.NetworkID]; !ok {
		return nil
	}

	err := deleteBridge(r.NetworkID)
	if err != nil {
		return err
	}

	delete(d.networks, r.NetworkID)

	return nil
}

func (d *BgpLB) AllocateNetwork(r *api.AllocateNetworkRequest) (*api.AllocateNetworkResponse, error) {
	return nil, nil
}

func (d *BgpLB) FreeNetwork(r *api.FreeNetworkRequest) error {
	return nil
}

func (d *BgpLB) CreateEndpoint(r *api.CreateEndpointRequest) (*api.CreateEndpointResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error if not in map */
	if _, ok := d.networks[r.NetworkID]; !ok {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	intfInfo := new(api.EndpointInterface)
	parsedMac, _ := net.ParseMAC(intfInfo.MacAddress)

	endpoint := &bgpEndpoint{
		macAddress: parsedMac,
	}

	d.networks[r.NetworkID].endpoints[r.EndpointID] = endpoint

	resp := &api.CreateEndpointResponse{
		Interface: intfInfo,
	}

	return resp, nil
}

func (d *BgpLB) DeleteEndpoint(r *api.DeleteEndpointRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Skip if not in map (both network and endpoint) */
	if _, netOk := d.networks[r.NetworkID]; !netOk {
		return nil
	}

	if _, epOk := d.networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil
	}

	delete(d.networks[r.NetworkID].endpoints, r.EndpointID)

	return nil
}

func (d *BgpLB) EndpointInfo(r *api.InfoRequest) (*api.InfoResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.networks[r.NetworkID]; !netOk {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil, types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	endpointInfo := d.networks[r.NetworkID].endpoints[r.EndpointID]
	value := make(map[string]string)

	value["ip_address"] = ""
	value["mac_address"] = endpointInfo.macAddress.String()
	value["veth_outside"] = endpointInfo.vethOutside

	resp := &api.InfoResponse{
		Value: value,
	}

	return resp, nil
}

func (d *BgpLB) Join(r *api.JoinRequest) (*api.JoinResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.networks[r.NetworkID]; !netOk {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil, types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	endpointInfo := d.networks[r.NetworkID].endpoints[r.EndpointID]
	vethInside, vethOutside, err := createVethPair(endpointInfo.macAddress)
	if err != nil {
		return nil, err
	}

	if err := attachInterfaceToBridge(d.networks[r.NetworkID].bridgeName, vethOutside); err != nil {
		return nil, err
	}

	d.networks[r.NetworkID].endpoints[r.EndpointID].vethInside = vethInside
	d.networks[r.NetworkID].endpoints[r.EndpointID].vethOutside = vethOutside

	resp := &api.JoinResponse{
		InterfaceName: api.InterfaceName{
			SrcName:   vethInside,
			DstPrefix: "eth",
		},
	}

	return resp, nil
}

func (d *BgpLB) Leave(r *api.LeaveRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.networks[r.NetworkID]; !netOk {
		return types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	endpointInfo := d.networks[r.NetworkID].endpoints[r.EndpointID]

	if err := deleteVethPair(endpointInfo.vethOutside); err != nil {
		return err
	}

	return nil
}

func (d *BgpLB) DiscoverNew(r *api.DiscoveryNotification) error {
	return nil
}

func (d *BgpLB) DiscoverDelete(r *api.DiscoveryNotification) error {
	return nil
}

func (d *BgpLB) ProgramExternalConnectivity(r *api.ProgramExternalConnectivityRequest) error {
	return nil
}

func (d *BgpLB) RevokeExternalConnectivity(r *api.RevokeExternalConnectivityRequest) error {
	return nil
}

func main() {
	d := &BgpLB{
		scope: "local",
		networks: map[string]*bgpNetwork{},
	}
	h := api.NewHandler(d)
	logrus.Infof("Starting Docker BGP LB Plugin")
	h.ServeUnix("bgplb", 0)
}
