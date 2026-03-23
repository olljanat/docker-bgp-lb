package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const (
	dockerDriverName = "ollijanatuinen/docker-bgp-lb:v1.7"
	SIGUSR2Number    = "12"
)

var (
	SIGUSR2Action  string
	SIGUSR2Enabled bool
)

func advertiseNetworksOnStart(ctx context.Context) {
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	err := fmt.Errorf("run once")
	for err != nil {
		_, err = cli.ServerVersion(ctx)
		if err != nil {
			time.Sleep(time.Second * 1)
		}
	}
	defer cli.Close()

	networkFilter := filters.NewArgs()
	networkFilter.Add("label", "bgplb_advertise=true")
	options := types.NetworkListOptions{
		Filters: networkFilter,
	}
	networks, err := cli.NetworkList(ctx, options)
	if err != nil {
		log.Errorf("advertiseNetworksOnStart: Error getting networks from Docker: %v\n", err)
		return
	}

	for _, network := range networks {
		log := log.WithField("network.id", network.ID[:11]).WithField("network.name", network.Name)
		ipamConfigs := network.IPAM.Config
		for _, ipam := range ipamConfigs {
			if err := addAdvertisedSubnet(ctx, network.ID, ipam.Subnet); err == nil {
				log.WithField("subnet", ipam.Subnet).Info("advertiseNetworksOnStart: advertising the subnet")
			} else {
				log.WithField("subnet", ipam.Subnet).Errorf("advertiseNetworksOnStart: failed to advertise the subnet: %v", err)
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

		network, err := cli.NetworkInspect(context.TODO(), networkID, types.NetworkInspectOptions{})
		if err != nil {
			log.Errorf("Cannot inspect network: %v", err)
		}

		if len(network.Containers) == 0 {
			log.Errorf("No containers found from network %s, skipping BGP route", network.Name)
			cli.Close()
			return false
		}

		for containerID, endpoint := range network.Containers {
			if endpoint.EndpointID != endpointID {
				continue
			}
			container, _ := cli.ContainerInspect(context.TODO(), containerID)
			log.Infof("waitContainerHealthy, waiting container %s", container.Name)
			if container.State != nil {
				if container.State.Health != nil {
					if container.State.Health.Status == "healthy" {
						log.Infof("Container %s healthy, adding BGP route(s)", container.Name)
						cli.Close()
						return true
					}
					if container.State.Health.Status == "unhealthy" {
						log.Errorf("Container %s is unhealthy, skipping BGP route", container.Name)
						cli.Close()
						return false
					}
				} else {
					if container.State.Running == true {
						log.Infof("Container %s running, adding BGP route(s)", container.Name)
						cli.Close()
						return true
					}
					if container.State.Status != "created" {
						log.Errorf("Container %s failed to start, skipping BGP route", container.Name)
						cli.Close()
						return false
					}
				}
			}
		}
		cli.Close()
		time.Sleep(1 * time.Second)
	}
}

func delContainerRoutes(containerID string, cli *client.Client) {
	networkFilter := filters.NewArgs()
	networkFilter.Add("driver", dockerDriverName)
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

func watchDockerEvents(ctx context.Context) {
	if os.Getenv("SIGUSR2_HANDLER") == "true" {
		log.Info("Enabling SIGUSR2 signal handler")

		SIGUSR2Enabled = true
		SIGUSR2Action = (os.Getenv("SIGUSR2_ACTION"))
	}

	backoffConfig := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(1*time.Second),
		backoff.WithMultiplier(1.5),
		backoff.WithRandomizationFactor(0),
		backoff.WithMaxInterval(5*time.Second),
		backoff.WithMaxElapsedTime(0),
	)

	ticker := backoff.NewTicker(backoffConfig)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
			if err != nil {
				log.Errorf("watchDockerEvents: cannot connect to Docker: %v", err)
				break
			}

			eventFilters := filters.NewArgs(
				filters.Arg("type", "network"),
				filters.Arg("action", "create"),
				filters.Arg("action", "destroy"),
			)

			if SIGUSR2Enabled {
				eventFilters.Add("type", "container")
				eventFilters.Add("action", "kill")
			}

			eventOptions := types.EventsOptions{Filters: eventFilters}
			messages, errors := cli.Events(ctx, eventOptions)

		eventLoop:
			for {
				select {
				case event := <-messages:
					if event.Type == events.NetworkEventType {
						switch event.Action {
						case events.ActionCreate:
							handleDockerNetworkCreate(ctx, cli, &event)
						case events.ActionDestroy:
							handleDockerNetworkDestroy(ctx, &event)
						}
					}
					if event.Type == events.ContainerEventType {
						if event.Action == events.ActionKill {
							if SIGUSR2Enabled {
								handleDockerContainerKill(ctx, cli, &event)
							}
						}
					}

				case err := <-errors:
					log.Warnf("watchDockerEvents: restarting due to: %v", err)
					break eventLoop
				}
			}
			cli.Close()
			backoffConfig.Reset()

		case <-ctx.Done():
			log.Warnf("watchDockerEvents: exiting due to: %v", ctx.Err())
			return
		}
	}
}

func handleDockerNetworkCreate(ctx context.Context, cli *client.Client, event *events.Message) {
	log := log.WithField("network.id", event.Actor.ID[:11])
	network, err := cli.NetworkInspect(ctx, event.Actor.ID, types.NetworkInspectOptions{})
	if err != nil {
		log.Errorf("handleDockerNetworkCreate: cannot inspect the network: %v", err)
		return
	}
	log = log.WithField("network.name", network.Name)
	if l, ok := network.Labels["bgplb_advertise"]; ok && l == "true" {
		for _, ipam := range network.IPAM.Config {
			if err := addAdvertisedSubnet(ctx, network.ID, ipam.Subnet); err == nil {
				log.WithField("subnet", ipam.Subnet).Info("handleDockerNetworkCreate: advertising the subnet")
			} else {
				log.WithField("subnet", ipam.Subnet).Errorf("handleDockerNetworkCreate: failed to advertise the subnet: %v", err)
			}
		}
	}
}

func handleDockerNetworkDestroy(ctx context.Context, event *events.Message) {
	log := log.WithField("network.id", event.Actor.ID[:11])
	if err := delAdvertisedNetwork(ctx, event.Actor.ID); err == nil {
		log.Info("handleDockerNetworkDestroy: removed the advertised network")
	} else {
		log.Errorf("handleDockerNetworkDestroy: failed to remove the advertised network: %v", err)
	}
}

func handleDockerContainerKill(ctx context.Context, cli *client.Client, event *events.Message) {
	log := log.WithField("container.id", event.Actor.ID[:11])
	if event.Actor.Attributes["signal"] == SIGUSR2Number {
		log.Info("SIGUSR2 signal received. Gracefully drain the load")
		delContainerRoutes(event.Actor.ID, cli)

		if SIGUSR2Action == "stop" {
			log.Info("Stopping the container due to the 'SIGUSR2_ACTION=stop'")
			go func() {
				time.Sleep(5 * time.Second)
				cli.ContainerStop(ctx, event.Actor.ID, container.StopOptions{Signal: "SIGTERM"})
			}()
		}
	}
}
