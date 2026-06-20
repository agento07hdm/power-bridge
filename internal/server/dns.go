package server

import (
	"encoding/binary"
	"log"
	"net"
	"os/exec"
	"strings"
)

// ListenDNSCaptivePortal listens on UDP addr (e.g. ":5353") and answers every
// DNS A-query with the AP IP (192.168.4.1) when in AP mode.  In normal mode
// all queries receive NXDOMAIN.  iptables redirects port 53 from AP clients
// to this port so dnsmasq config is not required for DNS to work.
func (s *Server) ListenDNSCaptivePortal(addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("DNS captive portal listening on %s (UDP)", addr)

	ip := net.ParseIP(apModeIP).To4()
	buf := make([]byte, 512)

	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		if n < 12 {
			continue
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		resp := buildDNSResponse(query, ip, isAPMode())
		if resp != nil {
			_, _ = conn.WriteTo(resp, src)
		}
	}
}

// buildDNSResponse builds a minimal DNS response for the given query.
// In AP mode every A question is answered with ip; otherwise NXDOMAIN.
func buildDNSResponse(query []byte, ip net.IP, apMode bool) []byte {
	if len(query) < 12 {
		return nil
	}

	txID := query[0:2]
	flags := uint16(0x8400) // QR + AA
	flags |= uint16(query[2]&0x01) << 8 // RD bit from query
	flags |= 0x0080                      // RA

	qdCount := binary.BigEndian.Uint16(query[4:6])
	if qdCount == 0 {
		return nil
	}

	// Skip QNAME to find QTYPE.
	offset := 12
	for offset < len(query) {
		l := int(query[offset])
		offset++
		if l == 0 {
			break
		}
		if l&0xC0 == 0xC0 {
			offset++
			break
		}
		offset += l
	}
	if offset+4 > len(query) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[offset : offset+2])

	var anCount uint16
	var answer []byte

	if apMode && qtype == 1 { // A record
		anCount = 1
		answer = make([]byte, 16)
		answer[0] = 0xC0
		answer[1] = 0x0C // pointer to question name at offset 12
		binary.BigEndian.PutUint16(answer[2:], 1)  // TYPE A
		binary.BigEndian.PutUint16(answer[4:], 1)  // CLASS IN
		binary.BigEndian.PutUint32(answer[6:], 0)  // TTL 0
		binary.BigEndian.PutUint16(answer[10:], 4) // RDLENGTH
		copy(answer[12:], ip)
	} else {
		flags |= 3 // NXDOMAIN
	}

	resp := make([]byte, 0, 12+len(query[12:])+len(answer))
	resp = append(resp, txID...)
	resp = append(resp, byte(flags>>8), byte(flags))
	resp = append(resp, query[4:6]...)                   // QDCOUNT
	resp = append(resp, byte(anCount>>8), byte(anCount)) // ANCOUNT
	resp = append(resp, 0, 0)                            // NSCOUNT
	resp = append(resp, 0, 0)                            // ARCOUNT
	resp = append(resp, query[12:]...)                   // question section
	resp = append(resp, answer...)
	return resp
}

// applyAPIPTables installs iptables NAT rules that redirect all DNS queries
// arriving on wlan0 to our DNS server on port 5353.  This works regardless of
// whether NM's internal dnsmasq reads our catch-all config.
func applyAPIPTables() {
	// Delete first so re-starts don't accumulate duplicate rules.
	cleanAPIPTables()

	rules := [][]string{
		{"-t", "nat", "-A", "PREROUTING", "-i", "wlan0", "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "5353"},
		{"-t", "nat", "-A", "PREROUTING", "-i", "wlan0", "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "5353"},
	}
	for _, args := range rules {
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			log.Printf("iptables add: %v (%s)", err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("iptables: added DNS redirect rule (wlan0:53 → :5353)")
		}
	}
}

// cleanAPIPTables removes the NAT rules added by applyAPIPTables.
func cleanAPIPTables() {
	rules := [][]string{
		{"-t", "nat", "-D", "PREROUTING", "-i", "wlan0", "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "5353"},
		{"-t", "nat", "-D", "PREROUTING", "-i", "wlan0", "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "5353"},
	}
	for _, args := range rules {
		_ = exec.Command("iptables", args...).Run()
	}
}
