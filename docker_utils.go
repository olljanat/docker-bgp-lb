package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	apiGoBGP "github.com/osrg/gobgp/v3/api"
)

const (
	driverName = "ollijanatuinen/docker-bgp-lb:v1.3"
	SIGUSR2    = "12"
)

func getGwBridge() {
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	err := fmt.Errorf("run once")
	for err != nil {
		_, err = cli.ServerVersion(context.Background())
		if err != nil {
			time.Sleep(time.Second * 1)
		}
	}

	networkFilter := filters.NewArgs()
	networkFilter.Add("label", "bgplb_advertise=true")
	options := types.NetworkListOptions{
		Filters: networkFilter,
	}
	networks, err := cli.NetworkList(context.Background(), options)
	if err != nil {
		log.Errorf("getGwBridge: Error getting networks from Docker: %v\n", err)
		return
	}

	for _, network := range networks {
		fmt.Printf("Network name: %v\r\n", network.Name)
		ipamConfigs := network.IPAM.Config
		for _, ipam := range ipamConfigs {
			_, ipnet, err := net.ParseCIDR(ipam.Subnet)
			if err != nil {
				log.Errorf("getGwBridge: Failed to parse IPAM subnet : %v\n", err)
				return
			}
			mask, _ := ipnet.Mask.Size()
			if ipnet.IP.To4() == nil && strings.Contains(ipnet.IP.String(), ":") {
				log.Infof("Adding BGP route to local BGP LB gateway IPv6 subnet: %v/%v", ipnet.IP.String(), mask)
				addBgpRoute(ipnet.IP.String(), mask, apiGoBGP.Family_AFI_IP6)
			} else {
				log.Infof("Adding BGP route to local BGP LB gateway IPv4 subnet: %v/%v", ipnet.IP.String(), mask)
				addBgpRoute(ipnet.IP.String(), mask, apiGoBGP.Family_AFI_IP)
			}
		}
	}
}

func waitContainerHealthy(networkID, endpointID string) bool {
	time.Sleep(1 * time.Second)
	for {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			log.Errorf("Cannot connect to Docker: %v", err)
		}

		network, err := cli.NetworkInspect(context.Background(), networkID, types.NetworkInspectOptions{})
		if err != nil {
			log.Errorf("Cannot inspect network: %v", err)
		}

		if len(network.Containers) == 0 {
			log.Errorf("No containers found from network %s, skipping BGP route", network.Name)
			return false
		}

		for containerID, endpoint := range network.Containers {
			if endpoint.EndpointID == endpointID {
				container, _ := cli.ContainerInspect(context.Background(), containerID)
				log.Infof("waitContainerHealthy, waiting container %s", container.Name)
				if container.State != nil {
					if container.State.Health != nil {
						if container.State.Health.Status == "healthy" {
							log.Infof("Container %s healthy, adding BGP route(s)", container.Name)
							return true
						}
						if container.State.Health.Status == "unhealthy" {
							log.Errorf("Container %s is unhealthy, skipping BGP route", container.Name)
							return false
						}
					} else {
						if container.State.Running == true {
							log.Infof("Container %s running, adding BGP route(s)", container.Name)
							return true
						}
						if container.State.Status != "created" {
							log.Errorf("Container %s failed to start, skipping BGP route", container.Name)
							return false
						}
					}
				}
			} else {
				return false
			}
		}
		time.Sleep(1 * time.Second)
	}
}

func watchDockerStopEvents() {
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	err := fmt.Errorf("run once")
	for err != nil {
		_, err = cli.ServerVersion(context.Background())
		if err != nil {
			time.Sleep(time.Second * 1)
		}
	}

	eventFilter := filters.NewArgs()
	eventFilter.Add("event", "kill")
	options := types.EventsOptions{
		Filters: eventFilter,
	}
	messages, errs := cli.Events(context.Background(), options)

	for {
		select {
		case event := <-messages:
			if event.Actor.Attributes["signal"] == SIGUSR2 {
				go stopContainer(event.ID, cli)
			}
		case err := <-errs:
			if err != nil {
				log.Errorf("watchDockerEvents: Error: %v\n", err)
				time.Sleep(1 * time.Second)
			}
		}
	}
}

func stopContainer(containerID string, cli *client.Client) {
	log.Infof("SIGUSR2 signal received from container ID %s, deleting BGP route(s)", containerID)
	delContainerRoutes(containerID, cli)
	time.Sleep(5 * time.Second)

	timeout := -1
	options := container.StopOptions{
		Signal:  "SIGTERM",
		Timeout: &timeout,
	}
	cli.ContainerStop(context.Background(), containerID, options)
}

func delContainerRoutes(containerID string, cli *client.Client) {
	networkFilter := filters.NewArgs()
	networkFilter.Add("driver", driverName)
	options := types.NetworkListOptions{
		Filters: networkFilter,
	}
	networks, err := cli.NetworkList(context.Background(), options)
	if err != nil {
		log.Errorf("delContainerRoutes: Error getting networks from Docker: %v\n", err)
		return
	}

	containerInspect, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		log.Errorf("delContainerRoutes: Error getting container details from Docker: %v\n", err)
		return
	}
	containerNetworks := containerInspect.NetworkSettings.Networks
	for _, containerNetwork := range containerNetworks {
		for _, network := range networks {
			if network.ID == containerNetwork.NetworkID {
				go delRoute(containerNetwork.NetworkID, containerNetwork.EndpointID)
			}
		}
	}
}
