package server

import (
	"encoding/binary"
	"log"
	"net"
)

// ListenDNSCaptivePortal listens on UDP addr (e.g. ":53") and answers every
// DNS A-query with the AP IP (192.168.4.1) when in AP mode.  Outside AP mode
// all queries are answered with NXDOMAIN so normal DNS still fails gracefully
// (the caller should not start this listener outside AP mode, but it is safe
// either way).
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
		query := buf[:n]
		resp := buildDNSResponse(query, ip, isAPMode())
		if resp != nil {
			_, _ = conn.WriteTo(resp, src)
		}
	}
}

// buildDNSResponse builds a minimal DNS response for the given query.
// If apMode is true every A question is answered with ip.
// Otherwise NXDOMAIN is returned.
func buildDNSResponse(query []byte, ip net.IP, apMode bool) []byte {
	if len(query) < 12 {
		return nil
	}

	// Copy header fields we need.
	txID := query[0:2]
	// QR=1 (response), opcode from query, AA=1, TC=0, RD from query, RA=1
	flags := uint16(0x8400) // QR + AA
	flags |= uint16(query[2]&0x01) << 8 // RD bit
	flags |= 0x0080                      // RA bit

	qdCount := binary.BigEndian.Uint16(query[4:6])
	if qdCount == 0 {
		return nil
	}

	// Parse the first question to get QTYPE.
	offset := 12
	// Skip QNAME (sequence of length-prefixed labels, ends with 0x00).
	for offset < len(query) {
		l := int(query[offset])
		offset++
		if l == 0 {
			break
		}
		if l&0xC0 == 0xC0 { // pointer – shouldn't appear in questions
			offset++
			break
		}
		offset += l
	}
	if offset+4 > len(query) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[offset : offset+2])

	// We only synthesise A records (qtype=1).  For everything else return
	// NXDOMAIN with zero answers so the OS doesn't hang waiting.
	var rcode uint16
	var anCount uint16

	var answer []byte
	if apMode && qtype == 1 {
		flags |= 0x0000 // NOERROR
		anCount = 1

		// Answer RR: name (pointer to question at offset 12), A, IN, TTL=0, RDLEN=4, IP.
		answer = make([]byte, 16)
		answer[0] = 0xC0
		answer[1] = 0x0C // pointer to offset 12 (start of question)
		binary.BigEndian.PutUint16(answer[2:], 1)    // TYPE A
		binary.BigEndian.PutUint16(answer[4:], 1)    // CLASS IN
		binary.BigEndian.PutUint32(answer[6:], 0)    // TTL 0
		binary.BigEndian.PutUint16(answer[10:], 4)   // RDLENGTH
		copy(answer[12:], ip)
	} else {
		rcode = 3 // NXDOMAIN
		flags |= rcode
	}

	resp := make([]byte, 0, 12+len(query[12:])+len(answer))
	resp = append(resp, txID...)
	resp = append(resp, byte(flags>>8), byte(flags))
	resp = append(resp, query[4:6]...)                           // QDCOUNT
	resp = append(resp, byte(anCount>>8), byte(anCount))         // ANCOUNT
	resp = append(resp, 0, 0)                                    // NSCOUNT
	resp = append(resp, 0, 0)                                    // ARCOUNT
	resp = append(resp, query[12:]...)                           // original question section
	resp = append(resp, answer...)
	return resp
}
