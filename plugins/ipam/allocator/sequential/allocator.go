// Copyright 2015 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sequential

import (
	"fmt"
	"log"
	"net"

	"github.com/containernetworking/cni/pkg/ip"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/plugins/ipam/store"
)

type IPAllocator struct {
	start net.IP
	end   net.IP
	conf  *IPAMConfig
	store backend.Store
}

func NewIPAllocator(conf *IPAMConfig, store backend.Store) (*IPAllocator, error) {
	var (
		start net.IP
		end   net.IP
		err   error
	)
	start, end, err = networkRange((*net.IPNet)(&conf.Subnet))
	if err != nil {
		return nil, err
	}

	// skip the .0 address
	start = ip.NextIP(start)

	if conf.RangeStart != nil {
		if err := validateRangeIP(conf.RangeStart, (*net.IPNet)(&conf.Subnet)); err != nil {
			return nil, err
		}
		start = conf.RangeStart
	}
	if conf.RangeEnd != nil {
		if err := validateRangeIP(conf.RangeEnd, (*net.IPNet)(&conf.Subnet)); err != nil {
			return nil, err
		}
		// RangeEnd is inclusive
		end = ip.NextIP(conf.RangeEnd)
	}
	return &IPAllocator{start, end, conf, store}, nil
}

func validateRangeIP(ip net.IP, ipnet *net.IPNet) error {
	if !ipnet.Contains(ip) {
		return fmt.Errorf("%s not in network: %s", ip, ipnet)
	}
	return nil
}

// Returns newly allocated IP along with its config
func (a *IPAllocator) Get(id string) (*types.IPConfig, error) {
	a.store.Lock()
	defer a.store.Unlock()

	gw := a.conf.Gateway
	if gw == nil {
		gw = ip.NextIP(a.conf.Subnet.IP)
	}

	var requestedIP net.IP
	if a.conf.Args != nil {
		requestedIP = a.conf.Args.IP
	}

	if requestedIP != nil {
		if gw != nil && gw.Equal(a.conf.Args.IP) {
			return nil, fmt.Errorf("requested IP must differ gateway IP")
		}

		subnet := net.IPNet{
			IP:   a.conf.Subnet.IP,
			Mask: a.conf.Subnet.Mask,
		}
		err := validateRangeIP(requestedIP, &subnet)
		if err != nil {
			return nil, err
		}

		reserved, err := a.store.Reserve(id, requestedIP)
		if err != nil {
			return nil, err
		}

		if reserved {
			return &types.IPConfig{
				IP:      net.IPNet{IP: requestedIP, Mask: a.conf.Subnet.Mask},
				Gateway: gw,
				Routes:  a.conf.Routes,
			}, nil
		}
		return nil, fmt.Errorf("requested IP address %q is not available in network: %s", requestedIP, a.conf.Name)
	}

	startIP, endIP := a.getSearchRange()
	for cur := startIP; !cur.Equal(endIP); cur = a.nextIP(cur) {
		// don't allocate gateway IP
		if gw != nil && cur.Equal(gw) {
			continue
		}

		reserved, err := a.store.Reserve(id, cur)
		if err != nil {
			return nil, err
		}
		if reserved {
			return &types.IPConfig{
				IP:      net.IPNet{IP: cur, Mask: a.conf.Subnet.Mask},
				Gateway: gw,
				Routes:  a.conf.Routes,
			}, nil
		}
	}
	return nil, fmt.Errorf("no IP addresses available in network: %s", a.conf.Name)
}

// Releases all IPs allocated for the container with given ID
func (a *IPAllocator) Release(id string) error {
	a.store.Lock()
	defer a.store.Unlock()

	return a.store.ReleaseByID(id)
}

func networkRange(ipnet *net.IPNet) (net.IP, net.IP, error) {
	if ipnet.IP == nil {
		return nil, nil, fmt.Errorf("missing field %q in IPAM configuration", "subnet")
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		ip = ipnet.IP.To16()
		if ip == nil {
			return nil, nil, fmt.Errorf("IP not v4 nor v6")
		}
	}

	if len(ip) != len(ipnet.Mask) {
		return nil, nil, fmt.Errorf("IPNet IP and Mask version mismatch")
	}

	var end net.IP
	for i := 0; i < len(ip); i++ {
		end = append(end, ip[i]|^ipnet.Mask[i])
	}
	return ipnet.IP, end, nil
}

// nextIP returns the next ip of curIP within ipallocator's subnet
func (a *IPAllocator) nextIP(curIP net.IP) net.IP {
	if curIP.Equal(a.end) {
		return a.start
	}
	return ip.NextIP(curIP)
}

// getSearchRange returns the start and end ip based on the last reserved ip
func (a *IPAllocator) getSearchRange() (net.IP, net.IP) {
	var startIP net.IP
	var endIP net.IP
	startFromLastReservedIP := false
	lastReservedIP, err := a.store.LastReservedIP()
	if err != nil {
		log.Printf("Error retriving last reserved ip: %v", err)
	} else if lastReservedIP != nil {
		subnet := net.IPNet{
			IP:   a.conf.Subnet.IP,
			Mask: a.conf.Subnet.Mask,
		}
		err := validateRangeIP(lastReservedIP, &subnet)
		if err == nil {
			startFromLastReservedIP = true
		}
	}
	if startFromLastReservedIP {
		startIP = a.nextIP(lastReservedIP)
		endIP = lastReservedIP
	} else {
		startIP = a.start
		endIP = a.end
	}
	return startIP, endIP
}
