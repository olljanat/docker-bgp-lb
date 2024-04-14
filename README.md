# About
This plugin provides integration with BGP capable network devices which removes need to do outgoing NAT for containers network connectivity and provide ECMP based load balancing between multiple hosts. More information about concept can be found from [RFC 7938](https://datatracker.ietf.org/doc/html/rfc7938) and from [Meta's blog](https://engineering.fb.com/2021/05/13/data-center-engineering/bgp/).

# Usage
## BGP router
You can use any BGP compatible router. If you don't have any, you can use these steps to setup lab.

Download [GoBGP](https://github.com/osrg/gobgp) binary.

Example config:
```toml
[global.config]
  as = 64512
  router-id = "192.168.8.137"

[[peer-groups]]
  [peer-groups.config]
    peer-group-name = "bgp-lb"
    peer-as = 65534
  [[peer-groups.afi-safis]]
    [peer-groups.afi-safis.config]
      afi-safi-name = "ipv4-unicast"
  [[peer-groups.afi-safis]]
    [peer-groups.afi-safis.config]
      afi-safi-name = "ipv4-flowspec"

[[dynamic-neighbors]]
  [dynamic-neighbors.config]
    prefix = "192.168.8.0/24"
    peer-group = "bgp-lb"
```
Run with command `./gobgpd --log-level=debug -f gobgp.toml`

## Gateway bridge network
Create host specific bridge network for outgoing connectivity (Like [docker_gwbridge](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge) but for non-swarm/non-overlay workloads):
```bash
docker network create \
  --driver bridge \
  --subnet 172.23.1.0/24 \
  --gateway 172.23.1.1 \
  --ipv6 \
  --subnet 2001:0db8:0000:1001::/64 \
  --gateway 2001:0db8:0000:1001::1 \
  -o com.docker.network.bridge.name=bgplb_gwbridge \
  -o com.docker.network.bridge.enable_icc=false \
  -o com.docker.network.bridge.enable_ip_masquerade=false \
  --label bgplb_advertise=true \
  bgplb_gwbridge
```
Label `bgplb_advertise=true` will tell bgplb driver to advertise it with BGP.
Option `com.docker.network.bridge.enable_ip_masquerade=false` will disable NAT from outgoing connections.
Option `com.docker.network.bridge.enable_icc=false` is optional, it will disable inter container connectivity.

## Plugin installation
```bash
docker plugin install \
  --grant-all-permissions \
  ollijanatuinen/docker-bgp-lb:v0.9 \
  ROUTER_ID=192.168.8.40 \
  LOCAL_AS=65534 \
  PEER_ADDRESS=192.168.8.137 \
  PEER_AS=64512 \
  SIGUSR2_HANDLER=true
```
GoBGP inform about incoming BGP connection with message like this:
```json
{
	"Key": "192.168.8.40",
	"Topic": "Peer",
	"level": "debug",
	"msg": "Accepted a new dynamic neighbor",
	"time": "2024-04-10T09:58:09Z"
}
```

## Creating LB networks and start containers
```bash
docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --subnet 10.0.0.101/32 \
  --ipv6 \
  --ipam-opt v6subnet=2001:0db8:0000:1000::101/128 \
   web1
docker run -d \
  --name=web1 \
  --network=bgplb_gwbridge \
  --network=web1 \
  --ip 172.23.0.25 \
  --ip6 2001:0db8:0000:1001::25 \
  --add-host web2=2001:0db8:0000:1000::102 \
  --health-cmd "curl -f http://localhost/ || exit 1" \
  --health-start-period 15s \
  --stop-timeout 30 \
  --stop-signal SIGUSR2 \
  ollijanatuinen/debug:nginx

docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --subnet 10.0.0.102/32 \
  --ipv6 \
  --ipam-opt v6subnet=2001:0db8:0000:1000::102/128 \
   web2
docker run -d \
  --name=web2 \
  --network=bgplb_gwbridge \
  --network=web2 \
  --ip 172.23.0.26 \
  --ip6 2001:0db8:0000:1001::26 \
  --add-host web1=2001:0db8:0000:1000::101 \
  --health-cmd "curl -f http://localhost/ || exit 1" \
  --health-start-period 15s \
  --stop-timeout 30 \
  --stop-signal SIGUSR2 \
  ollijanatuinen/debug:nginx
```

After containers are in "healthy" state two things will happen:
1. New local routes like this are added:
```
Destination     Gateway         Genmask         Flags Metric Ref    Use Iface
10.0.0.101 0.0.0.0         255.255.255.255 UH    0      0        0 bgplb-f9bb8454b
10.0.0.102 0.0.0.0         255.255.255.255 UH    0      0        0 bgplb-f9bb8454c

Destination                    Next Hop                   Flag Met Ref Use If
2001:db8:0:1::101/128          ::                         U    1024 3     0 bgplb-f9bb8454b
2001:db8:0:1::101/128          ::                         U    1024 3     0 bgplb-f9bb8454c
```
2. GoBGP inform about new BGP route with messages like this:
```json
{
	"Key": "192.168.8.40",
	"Topic": "Peer",
	"attributes": [
		{
			"type": 1,
			"value": 0
		},
		{
			"type": 2,
			"as_paths": [
				{
					"segment_type": 2,
					"num": 1,
					"asns": [
						65534
					]
				}
			]
		},
		{
			"type": 3,
			"nexthop": "192.168.8.40"
		}
	],
	"level": "debug",
	"msg": "received update",
	"nlri": [
		{
			"prefix": "10.0.0.101/32"
		}
	],
	"time": "2024-04-10T10:04:59Z",
	"withdrawals": []
}
```

## Graceful shutdown
If you installed plugin with `SIGUSR2_HANDLER=true` and started container with `--stop-signal SIGUSR2` option, three things will happen:
1. GoBGP inform about removed BGP route with message like this:
```json
{
	"Data": {
		"nlri": {
			"prefix": "10.0.0.101/32"
		},
		"attrs": [
			{
				"type": 1,
				"value": 0
			},
			{
				"type": 2,
				"as_paths": [
					{
						"segment_type": 2,
						"num": 1,
						"asns": [
							65534
						]
					}
				]
			},
			{
				"type": 3,
				"nexthop": "192.168.8.40"
			}
		],
		"age": 1712743499,
		"withdrawal": true,
		"source-id": "192.168.8.40",
		"neighbor-ip": "192.168.8.40"
	},
	"Key": "192.168.8.40",
	"Topic": "Peer",
	"level": "debug",
	"msg": "From me, ignore",
	"time": "2024-04-10T10:10:28Z"
}
```
2. Local route to `10.0.0.101/32` will be removed.
3. After 5 seconds delay, normal container stop signal `SIGTERM` will be send to container and it will stop.

## Docker Swarm
### Preparation
In Swarm mode we only define our load balancer subnet for services.
Docker will automatically add `docker_gwbridge` as second network for them which those containers uses for outgoing traffic.
To make those connections also using routed connectivity without NAT, we need reconfigure that network like described in [here](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge).
```bash
docker network rm docker_gwbridge
docker network create \
  --driver bridge \
  --subnet 172.23.2.0/24 \
  --gateway 172.23.2.1 \
  --ipv6 \
  --subnet 2001:0db8:0000:1002::/64 \
  --gateway 2001:0db8:0000:1002::1 \
  -o com.docker.network.bridge.name=docker_gwbridge \
  -o com.docker.network.bridge.enable_icc=false \
  -o com.docker.network.bridge.enable_ip_masquerade=false \
  --label bgplb_advertise=true \
  docker_gwbridge
```
**Note!** It is easiest to do this when node is not yet as part of swarm because other why you need remove and recreate also ingress network which affects all nodes same time (however, we are not actually using ingress at all in this configuration).
On current implementation, on this point you also need disable and re-enable bgp-lb plugin to trigger that network BGP advertise.

### Deployment
In Swarm mode we want to give two extra parameters:
* `--endpoint-mode=dnsrr` which disables VIP reservation done by Swarm so our IP address gets allocated directly to container.
* `--mode=global` which makes one replica of container running on every node in Swarm which have bgp-lb plugin installed.
  * This is where BGP-LB shows its power because all of those nodes will start advertising our load balancer IPs with BGP.
  * You can still limit this to certain nodes with `--constraint` parameter.
```bash
docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.9 \
  --subnet 10.0.0.103/32 \
  --ipv6 \
  --ipam-opt v6subnet=2001:0db8:0000:1000::103/128 \
   web

docker service create \
  --name web \
  --network=web \
  --endpoint-mode=dnsrr \
  --mode=global \
  ollijanatuinen/debug:nginx
```

## IPv6 only mode
Docker does not currently support disabling IPv4 which why yours containers always has IPv4 address in `bgplb_gwbridge` and `docker_gwbridge` networks.
However, you can skip configuring by IPv4 address for load balancing interface by simply skipping `--subnet` parameter when creating load balancing subnet and only specify `--ipam-opt v6subnet=`

Technically network will still have `0.0.0.0/32` configured as IPv4 subnet and it will get assigned to containers but Linux ignore it and this plugin will not advertise it with BGP.
