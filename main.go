package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/docker/docker/libnetwork/types"
	"github.com/olljanat/docker-bgp-lb/api"
	"github.com/sirupsen/logrus"
	apiGoBGP "github.com/osrg/gobgp/v3/api"
	loggerGoBGP "github.com/osrg/gobgp/v3/pkg/log"
	serverGoBGP "github.com/osrg/gobgp/v3/pkg/server"
	apb "google.golang.org/protobuf/types/known/anypb"
)

var scs = spew.ConfigState{Indent: "  "}
var bgpServer = serverGoBGP.BgpServer{}
var log = logrus.Logger{}

var routerid = ""
var localAs = uint32(0)

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

	/*
	_, ipnet, err := net.ParseCIDR(r.Pool)
	if err != nil {
		return &api.RequestPoolResponse{}, err
	}
	mask, _ := ipnet.Mask.Size()
	if mask != 32 {
		return &api.RequestPoolResponse{}, errors.New("Only subnet mask /32 is supported")
	}
	*/

	return &api.RequestPoolResponse{PoolID: r.Pool, Pool: r.Pool}, nil
}

func (d *BgpLB) RequestAddress(r *api.RequestAddressRequest) (*api.RequestAddressResponse, error) {
	/*
	if r.Options["RequestAddressType"] == "com.docker.network.gateway" {
		return &api.RequestAddressResponse{Address: r.PoolID}, nil
	}
	*/
	if r.Address == "" {
		return &api.RequestAddressResponse{}, errors.New("IP is required")
	}

	_, ipnet, err := net.ParseCIDR(r.PoolID)
	if err != nil {
		return &api.RequestAddressResponse{}, err
	}
	mask, _ := ipnet.Mask.Size()
	addr := fmt.Sprintf("%s/%s", r.Address, strconv.Itoa(mask))



	// Advertise LB IPs with /32 mask to BGP peer
	log.Infof("RequestAddress, Adding %v/32 to BGP", r.Address)
	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    r.Address,
		PrefixLen: 32,
	})

	a1, _ := apb.New(&apiGoBGP.OriginAttribute{
		Origin: 0,
	})
	a2, _ := apb.New(&apiGoBGP.NextHopAttribute{
		NextHop: routerid,
	})
	a3, _ := apb.New(&apiGoBGP.AsPathAttribute{
		Segments: []*apiGoBGP.AsSegment{
			{
				Type:    2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	_, err = bgpServer.AddPath(context.Background(), &apiGoBGP.AddPathRequest{
		Path: &apiGoBGP.Path{
			Family: &apiGoBGP.Family{Afi: apiGoBGP.Family_AFI_IP, Safi: apiGoBGP.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	if err != nil {
		return &api.RequestAddressResponse{}, err
	}


	return &api.RequestAddressResponse{Address: addr}, nil
}

func (d *BgpLB) ReleaseAddress(r *api.ReleaseAddressRequest) error {
	rFormatted := scs.Sdump(r)
	log.Infof(rFormatted)
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
	log = *logrus.New()
	log.SetLevel(logrus.DebugLevel)
	log.Out = os.Stdout

	routerid = os.Getenv("ROUTER_ID")
	if routerid == "" && net.ParseIP(routerid) == nil {
		log.Errorf("Environment variable ROUTER_ID is required\r\n")
		return
	}

	localAsInt, err := strconv.Atoi(os.Getenv("LOCAL_AS"))
	if err != nil {
		log.Errorf("Environment variable LOCAL_AS value is invalid\r\n")
		return
	}
	localAs = uint32(localAsInt)

	peerAddress := os.Getenv("PEER_ADDRESS")
	if peerAddress == "" && net.ParseIP(peerAddress) == nil {
		log.Errorf("Environment variable PEER_ADDRESS is required\r\n")
		return
	}

	peerAs, err := strconv.Atoi(os.Getenv("PEER_AS"))
	if err != nil {
		log.Errorf("Environment variable PEER_AS value is invalid\r\n")
		return
	}

	log.Infof("Starting BGP server")
	bgpLogger := loggerGoBGP.NewDefaultLogger()
	bgpServer = *serverGoBGP.NewBgpServer(serverGoBGP.LoggerOption(bgpLogger))
	go bgpServer.Serve()
	err = bgpServer.StartBgp(context.Background(), &apiGoBGP.StartBgpRequest{
		Global: &apiGoBGP.Global{
			RouterId:   routerid,
			Asn:        localAs,
			ListenPort: -1, // Passive mode, do not listen incoming BGP
		},
	})
	if err != nil {
		log.Errorf("StartBgp failed: %v", err)
		return
	}

	n := &apiGoBGP.Peer{
		Conf: &apiGoBGP.PeerConf{
			NeighborAddress: peerAddress,
			PeerAsn:          uint32(peerAs),
		},
	}
	if err := bgpServer.AddPeer(context.Background(), &apiGoBGP.AddPeerRequest{
		Peer: n,
	}); err != nil {
		log.Errorf("Adding BGP peer failed: %v", err)
		return
	}


	log.Infof("Starting Docker BGP LB Plugin")
	d := &BgpLB{
		scope: "local",
		networks: map[string]*bgpNetwork{},
	}
	h := api.NewHandler(d)
	if err = h.ServeUnix("bgplb", 0); err != nil {
		log.Errorf("ServeUnix failed: %v", err)
		return
	}
}
