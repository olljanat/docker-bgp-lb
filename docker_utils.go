package main

import (
	"context"
	"os"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const (
	driverName = "ollijanatuinen/docker-bgp-lb:v0.3"
	SIGUSR2    = "12"
)

func waitContainerHealthy(networkID, endpointID string) {
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

		for containerID, endpoint := range network.Containers {
			if endpoint.EndpointID == endpointID {
				container, _ := cli.ContainerInspect(context.Background(), containerID)
				if container.State != nil {
					if container.State.Health != nil {
						if container.State.Health.Status == "healthy" {
							log.Infof("Container %s healthy, adding BGP route(s)", container.Name)
							return
						}
					} else {
						if container.State.Running == true {
							log.Infof("Container %s running, adding BGP route(s)", container.Name)
							return
						}
					}
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
}

func watchDockerStopEvents() {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Errorf("watchDockerEvents: Error creating Docker client: %v\n", err)
		os.Exit(1)
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
