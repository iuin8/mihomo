//go:build !no_easytier

package outbound

import (
	"context"
	"errors"

	"github.com/metacubex/mihomo/component/easytier"
	D "github.com/miekg/dns"
)

type easyTierDNSTransport struct {
	easytier *EasyTier
}

func (t easyTierDNSTransport) Address() string {
	return "easytier://" + t.easytier.Name()
}

func (t easyTierDNSTransport) ResetConnection() {}

func (t easyTierDNSTransport) ExchangeContext(ctx context.Context, msg *D.Msg) (*D.Msg, error) {
	if len(msg.Question) == 0 {
		return nil, errors.New("should have one question at least")
	}
	if err := t.easytier.ensureStarted(ctx); err != nil {
		return nil, err
	}
	q := msg.Question[0]
	nodes, err := t.easytier.overlayNodes(ctx)
	if err != nil {
		return nil, err
	}
	reply := new(D.Msg)
	reply.SetReply(msg)
	reply.Authoritative = true
	reply.RecursionAvailable = true
	switch q.Qtype {
	case D.TypeA:
		ip, ok := easytier.LookupOverlayHost(q.Name, t.easytier.zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		reply.Answer = append(reply.Answer, &D.A{
			Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: easyTierDNSTTL},
			A:   ip.AsSlice(),
		})
	case D.TypePTR:
		ip, ok := easytier.ParsePTRIPv4(q.Name)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		name, ok := easytier.LookupOverlayPTR(ip, t.easytier.zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		reply.Answer = append(reply.Answer, &D.PTR{
			Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypePTR, Class: D.ClassINET, Ttl: easyTierDNSTTL},
			Ptr: name,
		})
	default:
		reply.Rcode = D.RcodeSuccess
	}
	return reply, nil
}
