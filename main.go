package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/docker/docker/libnetwork/types"
	"github.com/olljanat/docker-bgp-lb/api"
	"github.com/sirupsen/logrus"
)

var driverScope = "local"
var lbServer *bgpLB
var log = &logrus.Logger{}
var scs = spew.ConfigState{Indent: "  "}
var stateFile = "/bgplb.json"

type bgpLBEndpoint struct {
	vethInside  string
	vethOutside string
}

type bgpNetwork struct {
	endpoints map[string]*bgpLBEndpoint
}

type advertisedNetwork struct {
	subnets []string
	sync.Mutex
}

type bgpLB struct {
	Networks map[string]*bgpNetwork

	advertisedNetworks map[string]*advertisedNetwork
	scope              string
	sync.Mutex
}

func (d *bgpLB) GetIpamCapabilities() (*api.CapabilitiesResponse, error) {
	return &api.CapabilitiesResponse{RequiresMACAddress: true}, nil
}

func (d *bgpLB) GetNetCapabilities() (*api.CapabilitiesResponse, error) {
	capabilities := &api.CapabilitiesResponse{
		Scope: d.scope,
	}

	return capabilities, nil
}

func (d *bgpLB) GetDefaultAddressSpaces() (*api.AddressSpacesResponse, error) {
	return &api.AddressSpacesResponse{LocalDefaultAddressSpace: api.GlobalScope,
		GlobalDefaultAddressSpace: api.GlobalScope}, nil
}

func (d *bgpLB) RequestPool(r *api.RequestPoolRequest) (*api.RequestPoolResponse, error) {
	pool := ""
	if r.V6 {
		if r.Options["v6subnet"] == "" {
			return &api.RequestPoolResponse{}, errors.New("IPv6 subnet is required")
		}
		pool = r.Options["v6subnet"]
	} else {
		if r.Pool == "" {
			return &api.RequestPoolResponse{PoolID: "0.0.0.0/32", Pool: "0.0.0.0/32"}, nil
		}
		pool = r.Pool
	}

	_, ipnet, err := net.ParseCIDR(pool)
	if err != nil {
		return &api.RequestPoolResponse{}, err
	}
	mask, _ := ipnet.Mask.Size()
	if !r.V6 && mask != 32 {
		return &api.RequestPoolResponse{}, errors.New("only subnet mask /32 is supported")
	}
	if r.V6 && mask != 128 {
		return &api.RequestPoolResponse{}, errors.New("only subnet mask /128 is supported")
	}

	return &api.RequestPoolResponse{PoolID: pool, Pool: pool}, nil
}

func (d *bgpLB) RequestAddress(r *api.RequestAddressRequest) (*api.RequestAddressResponse, error) {
	if r.Options["RequestAddressType"] == "com.docker.network.gateway" {
		return &api.RequestAddressResponse{Address: r.PoolID}, nil
	}

	return &api.RequestAddressResponse{Address: r.PoolID}, nil
}

func (d *bgpLB) ReleaseAddress(r *api.ReleaseAddressRequest) error {
	return nil
}

func (d *bgpLB) ReleasePool(r *api.ReleasePoolRequest) error {
	return nil
}

func (d *bgpLB) CreateNetwork(r *api.CreateNetworkRequest) error {
	d.Lock()
	defer d.Unlock()

	if _, ok := d.Networks[r.NetworkID]; ok {
		return types.ForbiddenErrorf("network %s exists", r.NetworkID)
	}

	err := createBridgeFromNetID(r.NetworkID)
	if err != nil {
		return err
	}

	bgpNetwork := &bgpNetwork{
		endpoints: make(map[string]*bgpLBEndpoint),
	}

	d.Networks[r.NetworkID] = bgpNetwork
	err = d.saveState()
	if err != nil {
		return err
	}

	return nil
}

func (d *bgpLB) DeleteNetwork(r *api.DeleteNetworkRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Skip if not in map */
	if _, ok := d.Networks[r.NetworkID]; !ok {
		return nil
	}

	err := deleteBridge(r.NetworkID)
	if err != nil {
		return err
	}

	delete(d.Networks, r.NetworkID)
	err = d.saveState()
	if err != nil {
		return err
	}

	return nil
}

func (d *bgpLB) AllocateNetwork(r *api.AllocateNetworkRequest) (*api.AllocateNetworkResponse, error) {
	return nil, nil
}

func (d *bgpLB) FreeNetwork(r *api.FreeNetworkRequest) error {
	return nil
}

func (d *bgpLB) CreateEndpoint(r *api.CreateEndpointRequest) (*api.CreateEndpointResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error if not in map */
	if _, ok := d.Networks[r.NetworkID]; !ok {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	d.Networks[r.NetworkID].endpoints[r.EndpointID] = &bgpLBEndpoint{}

	resp := &api.CreateEndpointResponse{}

	// Start Goroutine which will add local and BGP routes after container is up and running
	go addRoute(r.NetworkID, r.EndpointID, r.Interface.Address, r.Interface.AddressIPv6)

	return resp, nil
}

func (d *bgpLB) DeleteEndpoint(r *api.DeleteEndpointRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Skip if not in map (both network and endpoint) */
	if _, netOk := d.Networks[r.NetworkID]; !netOk {
		return nil
	}

	if _, epOk := d.Networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil
	}

	delete(d.Networks[r.NetworkID].endpoints, r.EndpointID)

	return nil
}

func (d *bgpLB) EndpointInfo(r *api.InfoRequest) (*api.InfoResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.Networks[r.NetworkID]; !netOk {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.Networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil, types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	endpointInfo := d.Networks[r.NetworkID].endpoints[r.EndpointID]
	value := make(map[string]string)

	value["ip_address"] = ""
	value["mac_address"] = ""
	value["veth_outside"] = endpointInfo.vethOutside

	resp := &api.InfoResponse{
		Value: value,
	}

	return resp, nil
}

func (d *bgpLB) Join(r *api.JoinRequest) (*api.JoinResponse, error) {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.Networks[r.NetworkID]; !netOk {
		return nil, types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.Networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return nil, types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	vethInside, vethOutside, err := createVethPair()
	if err != nil {
		return nil, err
	}

	if err := attachInterfaceToBridge(getBridgeNameByNetID(r.NetworkID), vethOutside); err != nil {
		return nil, err
	}

	d.Networks[r.NetworkID].endpoints[r.EndpointID].vethInside = vethInside
	d.Networks[r.NetworkID].endpoints[r.EndpointID].vethOutside = vethOutside

	resp := &api.JoinResponse{
		InterfaceName: api.InterfaceName{
			SrcName:   vethInside,
			DstPrefix: "eth",
		},
	}

	return resp, nil
}

func (d *bgpLB) Leave(r *api.LeaveRequest) error {
	d.Lock()
	defer d.Unlock()

	/* Throw error (both network and endpoint) */
	if _, netOk := d.Networks[r.NetworkID]; !netOk {
		return types.ForbiddenErrorf("%s network does not exist", r.NetworkID)
	}

	if _, epOk := d.Networks[r.NetworkID].endpoints[r.EndpointID]; !epOk {
		return types.ForbiddenErrorf("%s endpoint does not exist", r.NetworkID)
	}

	delRoute(r.NetworkID, r.EndpointID)

	endpointInfo := d.Networks[r.NetworkID].endpoints[r.EndpointID]

	if err := deleteVethPair(endpointInfo.vethOutside); err != nil {
		return err
	}

	return nil
}

func (d *bgpLB) DiscoverNew(r *api.DiscoveryNotification) error {
	return nil
}

func (d *bgpLB) DiscoverDelete(r *api.DiscoveryNotification) error {
	return nil
}

func (d *bgpLB) ProgramExternalConnectivity(r *api.ProgramExternalConnectivityRequest) error {
	return nil
}

func (d *bgpLB) RevokeExternalConnectivity(r *api.RevokeExternalConnectivityRequest) error {
	return nil
}

func (lb *bgpLB) saveState() error {
	data, err := json.Marshal(lb)
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0644)
}

func loadState() (*bgpLB, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	var b bgpLB
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	b.scope = driverScope
	return &b, nil
}

func addAdvertisedSubnet(ctx context.Context, netID, subnet string) error {
	net := &advertisedNetwork{}

	lbServer.Lock()
	if n, ok := lbServer.advertisedNetworks[netID]; !ok {
		lbServer.advertisedNetworks[netID] = net
	} else {
		net = n
	}
	lbServer.Unlock()

	net.Lock()
	if slices.Contains(net.subnets, subnet) {
		net.Unlock()
		return fmt.Errorf("addAdvertisedSubnet: the subnet '%s' is already advertised", subnet)
	}
	// reserve the subnet before the external call
	net.subnets = append(net.subnets, subnet)
	net.Unlock()

	if !isPrefixAdvertised(ctx, subnet) {
		if err := advertisePrefix(ctx, subnet); err != nil {
			net.Lock()
			// remove the reserved subnet if advertising failed
			idx := slices.Index(net.subnets, subnet)
			if idx >= 0 {
				net.subnets = slices.Delete(net.subnets, idx, idx+1)
			}
			net.Unlock()
			return fmt.Errorf("addAdvertisedSubnet: failed to advertise the subnet: %w", err)
		}
	}

	return nil
}

func delAdvertisedNetwork(ctx context.Context, netID string) error {
	lbServer.Lock()

	net, ok := lbServer.advertisedNetworks[netID]
	if !ok {
		lbServer.Unlock()
		return fmt.Errorf("delAdvertisedNetwork: network '%s' is not advertised", netID[:11])
	}
	lbServer.Unlock()

	net.Lock()
	subnets := append([]string(nil), net.subnets...)
	net.Unlock()

	for _, subnet := range subnets {
		if isPrefixAdvertised(ctx, subnet) {
			if err := withdrawPrefix(ctx, subnet); err != nil {
				return fmt.Errorf("delAdvertisedNetwork: failed to withdraw the subnet %w", err)
			}
		}
	}

	lbServer.Lock()
	delete(lbServer.advertisedNetworks, netID)
	lbServer.Unlock()
	return nil
}

func main() {
	log = &logrus.Logger{
		Out:   os.Stdout,
		Level: logrus.DebugLevel,
		Formatter: &logrus.TextFormatter{
			FullTimestamp:          true,
			DisableLevelTruncation: true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peerAddress := os.Getenv("PEER_ADDRESS")
	if peerAddress == "" {
		log.Error("Environment variable PEER_ADDRESS is required")
		return
	}

	if net.ParseIP(peerAddress) == nil {
		log.Errorf("Value of PEER_ADDRESS is not a valid IP address. Got: %s", peerAddress)
		return
	}

	if err := startBgpServer(peerAddress); err != nil {
		log.Errorf("Starting BGP server failed: %v", err)
		return
	}

	if os.Getenv("GLOBAL_SCOPE") == "true" {
		driverScope = "global"
	}

	log.Infof("Starting Docker BGP LB Plugin")

	lbServer = &bgpLB{
		advertisedNetworks: make(map[string]*advertisedNetwork),
		scope:              driverScope,
	}
	go advertiseNetworksOnStart(ctx)
	go watchDockerEvents(ctx)
	// Load saves networks configuration but only when we are not running in swarm mode.
	// This is because swarm will automatically create/remove networks when needed.
	lbServer.Lock()
	if driverScope == "global" {
		log.Info("Running in Swarm mode, starting with an empty configuration.")
		lbServer.Networks = make(map[string]*bgpNetwork)
	} else {
		d, err := loadState()
		if err != nil {
			log.Info("Failed to load data, starting with an empty configuration.")
			lbServer.Networks = make(map[string]*bgpNetwork)
		} else {
			lbServer.Networks = d.Networks
		}
	}

	for id, network := range lbServer.Networks {
		if err := createBridgeFromNetID(id); err != nil {
			log.Printf("Failed to create bridge for network %s: %v", id, err)
		}
		network.endpoints = make(map[string]*bgpLBEndpoint)
	}
	lbServer.Unlock()

	h := api.NewHandler(lbServer)
	if err := h.ServeUnix("bgplb", 0); err != nil {
		log.Errorf("ServeUnix failed: %v", err)
		return
	}
}
