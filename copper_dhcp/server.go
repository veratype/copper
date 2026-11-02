package copper_dhcp

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
)

// Server is a DHCPv4 server with MAC filters, pool, and reservations.
type Server struct {
	cfg   *Normalized
	store *Store
	srv   *server4.Server

	mu     sync.Mutex
	logger *log.Logger
}

// New builds a server from Config. Call Start to listen.
func New(cfg Config) (*Server, error) {
	n, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:    n,
		store:  NewStore(n),
		logger: log.Default(),
	}, nil
}

// SetLogger overrides the default logger.
func (s *Server) SetLogger(l *log.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Store exposes the lease table (for APIs / debugging).
func (s *Server) Store() *Store { return s.store }

// Listen binds the UDP socket (needs privileges for port 67). Call Serve after.
func (s *Server) Listen() error {
	addr := &net.UDPAddr{IP: s.cfg.ListenHost, Port: s.cfg.ListenPort}
	srv, err := server4.NewServer(s.cfg.Interface, addr, s.handle, server4.WithSummaryLogger())
	if err != nil {
		return fmt.Errorf("dhcp listen %s (iface=%q): %w", addr, s.cfg.Interface, err)
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	s.logger.Printf("dhcp listening on %s iface=%q pool=%s-%s allow_all=%v",
		addr, s.cfg.Interface, s.cfg.RangeStart, s.cfg.RangeEnd, s.cfg.AllowAll)
	return nil
}

// Serve blocks until Close. Listen must have succeeded.
func (s *Server) Serve() error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("dhcp: Listen was not called")
	}
	return srv.Serve()
}

// Start binds then serves forever (blocks). Prefer Listen+Serve with Manager.
func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

// Close stops the UDP listener.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	s.srv = nil
	return err
}

func (s *Server) handle(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	if req == nil || req.OpCode != dhcpv4.OpcodeBootRequest {
		return
	}
	mac := req.ClientHWAddr
	if !s.cfg.MACAllowed(mac) {
		s.logger.Printf("dhcp ignore %s mac=%s (filtered)", req.MessageType(), mac)
		return
	}

	switch req.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		s.replyOffer(conn, peer, req)
	case dhcpv4.MessageTypeRequest:
		s.replyRequest(conn, peer, req)
	case dhcpv4.MessageTypeRelease:
		s.store.Release(mac)
		s.logger.Printf("dhcp release mac=%s", mac)
	case dhcpv4.MessageTypeDecline:
		s.store.Decline(mac, req.RequestedIPAddress())
		s.logger.Printf("dhcp decline mac=%s ip=%s", mac, req.RequestedIPAddress())
	default:
		// Inform / others: ignore for now
	}
}

func (s *Server) replyOffer(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	ip, err := s.store.Allocate(req.ClientHWAddr, req.RequestedIPAddress())
	if err != nil {
		s.logger.Printf("dhcp offer failed mac=%s: %v", req.ClientHWAddr, err)
		return
	}
	reply, err := s.buildReply(req, dhcpv4.MessageTypeOffer, ip)
	if err != nil {
		s.logger.Printf("dhcp offer build: %v", err)
		return
	}
	s.send(conn, peer, reply)
	s.logger.Printf("dhcp offer mac=%s ip=%s", req.ClientHWAddr, ip)
}

func (s *Server) replyRequest(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	// If client is talking to another server, stay quiet.
	if sid := req.ServerIdentifier(); sid != nil && !sid.Equal(s.cfg.ServerIP) {
		return
	}
	prefer := req.RequestedIPAddress()
	if prefer == nil {
		prefer = req.ClientIPAddr
	}
	ip, err := s.store.Confirm(req.ClientHWAddr, prefer)
	if err != nil {
		nak, nerr := s.buildReply(req, dhcpv4.MessageTypeNak, nil)
		if nerr == nil {
			s.send(conn, peer, nak)
		}
		s.logger.Printf("dhcp nak mac=%s: %v", req.ClientHWAddr, err)
		return
	}
	// If client requested a specific IP we could not honour exactly, NAK.
	if prefer != nil && prefer.To4() != nil && !prefer.Equal(ip) {
		nak, nerr := s.buildReply(req, dhcpv4.MessageTypeNak, nil)
		if nerr == nil {
			s.send(conn, peer, nak)
		}
		s.logger.Printf("dhcp nak mac=%s wanted=%s got=%s", req.ClientHWAddr, prefer, ip)
		return
	}
	reply, err := s.buildReply(req, dhcpv4.MessageTypeAck, ip)
	if err != nil {
		s.logger.Printf("dhcp ack build: %v", err)
		return
	}
	s.send(conn, peer, reply)
	s.logger.Printf("dhcp ack mac=%s ip=%s", req.ClientHWAddr, ip)
}

func (s *Server) buildReply(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, ip net.IP) (*dhcpv4.DHCPv4, error) {
	mods := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		// siaddr = next-server (PXE "where to go"); option 54 stays ServerIP.
		dhcpv4.WithServerIP(s.cfg.NextServer),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(s.cfg.LeaseTime)),
	}
	if mt != dhcpv4.MessageTypeNak && ip != nil {
		mods = append(mods,
			dhcpv4.WithYourIP(ip),
			dhcpv4.WithOption(dhcpv4.OptSubnetMask(s.cfg.Mask)),
		)
		if s.cfg.Gateway != nil {
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptRouter(s.cfg.Gateway)))
		}
		if len(s.cfg.DNS) > 0 {
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptDNS(s.cfg.DNS...)))
		}
		// Option 66 for firmware that ignores siaddr; same IP as next-server.
		if s.cfg.NextServer != nil {
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptTFTPServerName(s.cfg.NextServer.String())))
		}
		if s.cfg.BootFile != "" {
			bf := s.cfg.BootFile
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptBootFileName(bf)))
			// Legacy header "file" field (128 bytes). Long HTTP URLs stay in option 67 only.
			if len(bf) < 128 {
				mods = append(mods, func(d *dhcpv4.DHCPv4) { d.BootFileName = bf })
			}
		}
	}
	return dhcpv4.NewReplyFromRequest(req, mods...)
}

func (s *Server) send(conn net.PacketConn, peer net.Addr, reply *dhcpv4.DHCPv4) {
	if _, err := conn.WriteTo(reply.ToBytes(), peer); err != nil {
		s.logger.Printf("dhcp send to %v: %v", peer, err)
	}
}
