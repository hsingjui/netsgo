package client

import (
	"encoding/json"
	"net"

	"netsgo/internal/ingresspolicy"
)

type ingressAccessPolicy struct {
	sourceCIDRs []*net.IPNet
}

func parseIngressAccessPolicy(values []string, allowMissing bool) (ingressAccessPolicy, error) {
	policy, err := ingresspolicy.Parse(values, allowMissing)
	if err != nil {
		return ingressAccessPolicy{}, err
	}
	return ingressAccessPolicy{sourceCIDRs: policy.SourceCIDRs}, nil
}

func decodeIngressAccessPolicy(raw json.RawMessage, allowMissing bool) (ingressAccessPolicy, error) {
	policy, err := ingresspolicy.Decode(raw, allowMissing)
	if err != nil {
		return ingressAccessPolicy{}, err
	}
	return ingressAccessPolicy{sourceCIDRs: policy.SourceCIDRs}, nil
}

func sourceAddrAllowed(addr net.Addr, cidrs []*net.IPNet) bool {
	return ingresspolicy.SourceAllowed(addr, cidrs)
}
