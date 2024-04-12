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

## Plugin installation
```bash
docker plugin install \
  --grant-all-permissions \
  ollijanatuinen/docker-bgp-lb:v0.6 \
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

## Gateway bridge network
Create host specific bridge network for outgoing connectivity (Like [docker_gwbridge](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge) but for non-swarm/non-overlay workloads):
```bash
docker network create \
  --driver bridge \
  --subnet 172.23.0.0/24 \
  --gateway 172.23.0.1 \
  --ipv6 \
  --subnet 2001:db8::0/64 \
  --gateway 2001:db8::1 \
  -o com.docker.network.bridge.name=bgplb_gwbridge \
  -o com.docker.network.bridge.enable_icc=false \
  -o com.docker.network.bridge.enable_ip_masquerade=false \
  --label bgplb_advertise=true \
  bgplb_gwbridge
```
Label `bgplb_advertise=true` will tell bgplb driver to advertise it with BGP.
Option `com.docker.network.bridge.enable_ip_masquerade=false` will disable NAT from outgoing connections.
Option `com.docker.network.bridge.enable_icc=false` is optional, it will disable inter container connectivity.

## Creating LB networks and start containers
```bash
docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.6 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.6 \
  --subnet 10.0.0.101/32 \
  --ipv6 \
  --ipam-opt v6subnet=2001:0db8:0000:0001::101/128 \
   web1
docker run -d \
  --name=web1 \
  --network=bgplb_gwbridge \
  --network=web1 \
  --ip 172.23.0.25 \
  --ip6 2001:db8::25 \
  --add-host web2=2001:0db8:0000:0001::102 \
  --health-cmd "curl -f http://localhost/ || exit 1" \
  --health-start-period 15s \
  --stop-timeout 30 \
  --stop-signal SIGUSR2 \
  ollijanatuinen/debug:nginx

docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.6 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.6 \
  --subnet 10.0.0.102/32 \
  --ipv6 \
  --ipam-opt v6subnet=2001:0db8:0000:0001::102/128 \
   web2
docker run -d \
  --name=web2 \
  --network=bgplb_gwbridge \
  --network=web2 \
  --ip 172.23.0.26 \
  --add-host web1=2001:0db8:0000:0001::102 \
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
