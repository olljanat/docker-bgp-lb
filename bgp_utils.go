package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	apiGoBGP "github.com/osrg/gobgp/v3/api"
	loggerGoBGP "github.com/osrg/gobgp/v3/pkg/log"
	serverGoBGP "github.com/osrg/gobgp/v3/pkg/server"
	"github.com/vishvananda/netlink"
	apb "google.golang.org/protobuf/types/known/anypb"
)

var bgpServer = serverGoBGP.BgpServer{}
var routerid = ""
var localAs = uint32(0)

func startBgpServer() error {
	routerid = os.Getenv("ROUTER_ID")
	if routerid == "" && net.ParseIP(routerid) == nil {
		return fmt.Errorf("Environment variable ROUTER_ID is required\r\n")
	}

	localAsInt, err := strconv.Atoi(os.Getenv("LOCAL_AS"))
	if err != nil {
		return fmt.Errorf("Environment variable LOCAL_AS value is invalid\r\n")
	}
	localAs = uint32(localAsInt)

	peerAddress := os.Getenv("PEER_ADDRESS")
	if peerAddress == "" && net.ParseIP(peerAddress) == nil {
		return fmt.Errorf("Environment variable PEER_ADDRESS is required\r\n")
	}

	peerAsInt, err := strconv.Atoi(os.Getenv("PEER_AS"))
	if err != nil {
		return fmt.Errorf("Environment variable PEER_AS value is invalid\r\n")
	}
	peerAs := uint32(peerAsInt)

	peerPassword := os.Getenv("PEER_PASSWORD")
	if peerPassword == "" {
		return fmt.Errorf("Environment variable PEER_PASSWORD is required\r\n")
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
		return err
	}

	n := &apiGoBGP.Peer{
		Conf: &apiGoBGP.PeerConf{
			NeighborAddress: peerAddress,
			PeerAsn:         peerAs,
			AuthPassword:    peerPassword,
		},
	}
	if err := bgpServer.AddPeer(context.Background(), &apiGoBGP.AddPeerRequest{
		Peer: n,
	}); err != nil {
		return err
	}

	return nil
}

func addRoute(NetworkID, EndpointID, ipv4, ipv6 string) {
	waitContainerHealthy(NetworkID, EndpointID)

	bridgeName := getBridgeName(NetworkID)
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		log.Errorf("addRoute error: %v", err)
		return
	}
	if ipv4 != "" {
		ip, ipv4Dst, _ := net.ParseCIDR(ipv4)
		route := netlink.Route{Dst: ipv4Dst, LinkIndex: bridge.Attrs().Index}
		netlink.RouteAdd(&route)

		addBgpRoute(ip.String(), 32, apiGoBGP.Family_AFI_IP)
	}
	if ipv6 != "" {
		ip, ipv6Dst, _ := net.ParseCIDR(ipv6)
		route := netlink.Route{Dst: ipv6Dst, LinkIndex: bridge.Attrs().Index}
		netlink.RouteAdd(&route)

		addBgpRoute(ip.String(), 128, apiGoBGP.Family_AFI_IP6)
	}

	return
}

func addBgpRoute(prefix string, mask int, ipFamily apiGoBGP.Family_Afi) error {
	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    prefix,
		PrefixLen: uint32(mask),
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
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	_, err := bgpServer.AddPath(context.Background(), &apiGoBGP.AddPathRequest{
		Path: &apiGoBGP.Path{
			Family: &apiGoBGP.Family{Afi: ipFamily, Safi: apiGoBGP.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	return err
}

func delRoute(NetworkID, EndpointID string) {
	bridgeName := getBridgeName(NetworkID)
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		log.Errorf("delRoute error: %v", err)
		return
	}

	v4routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		log.Errorf("Failed to get local IPv4 routes: %v", err)
	}
	for _, v4route := range v4routes {
		v4dst := &v4route.Dst.IP
		delBgpRoute(v4dst.String(), 32, apiGoBGP.Family_AFI_IP)
		if err := netlink.RouteDel(&v4route); err != nil {
			log.Errorf("Cannot remove local route to: %v , Error: %v", v4dst.String(), err)
		}
	}
	v6routes, err := netlink.RouteList(bridge, netlink.FAMILY_V6)
	if err != nil {
		log.Errorf("Failed to get local IPv6 routes: %v", err)
	}
	for _, v6route := range v6routes {
		v6dst := &v6route.Dst.IP
		delBgpRoute(v6dst.String(), 128, apiGoBGP.Family_AFI_IP6)
		if err := netlink.RouteDel(&v6route); err != nil {
			log.Errorf("Cannot remove local route to: %v , Error: %v", v6dst.String(), err)
		}
	}

	return
}

func delBgpRoute(prefix string, mask int, ipFamily apiGoBGP.Family_Afi) error {
	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    prefix,
		PrefixLen: uint32(mask),
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
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	p1 := &apiGoBGP.Path{
		Family: &apiGoBGP.Family{Afi: ipFamily, Safi: apiGoBGP.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: attrs,
	}
	err := bgpServer.DeletePath(context.Background(), &apiGoBGP.DeletePathRequest{
		TableType: apiGoBGP.TableType_GLOBAL,
		Path:      p1,
	})
	if err != nil {
		return err
	}
	return nil
}
