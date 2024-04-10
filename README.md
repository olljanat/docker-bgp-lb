# About
## Prerequirements
`/etc/docker/daemon.json` file:
```json
{
  "bridge": "none",
  "iptables": false
}
```

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
  ollijanatuinen/docker-bgp-lb:v0.3 \
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

## Creating network and starting container
```bash
docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.3 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.3 \
  --subnet 200.200.200.200/32 \
  example

docker run -d \
  --name=example \
  --net=example \
  --health-cmd "curl -f http://localhost/ || exit 1" \
  --health-start-period 15s \
  --stop-timeout 30 \
  --stop-signal SIGUSR2 \
  nginx
```

After container is in "healthy" state two things will happen:
1. New local route like this is added:
```
Destination     Gateway         Genmask         Flags Metric Ref    Use Iface
200.200.200.200 0.0.0.0         255.255.255.255 UH    0      0        0 bgplb-f9bb8454b
```
2. GoBGP inform about new BGP route with message like this:
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
			"prefix": "200.200.200.200/32"
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
			"prefix": "200.200.200.200/32"
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
2. Local route to `200.200.200.200/32` will be removed.
3. After 5 seconds delay, normal container stop signal `SIGTERM` will be send to container and it will stop.

# Architecture
Combine bridge plugin from [Kathará](https://github.com/KatharaFramework/NetworkPlugin) together with [Sample IPAM plugin](https://github.com/ishantt/docker-ipam-plugin) and [GoBGP](https://github.com/osrg/gobgp/). Pure minimum implementation without any IP selection logic (=> user must tell IPs).

Does NOT configure default gateway for containers which trigger Docker adding second [docker_gwbridge](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge) interface as default gateway for them which why we have use this plugin to define load balancer IPs only. Alternatively you can add second, bridge network for containers.
