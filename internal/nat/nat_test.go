package nat

import (
	"encoding/binary"
	"encoding/xml"
	"net"
	"testing"
)

// TestParseBindingXOR reads a binding response as a server writes it.
func TestParseBindingXOR(t *testing.T) {
	ip := net.IPv4(203, 0, 113, 7).To4()
	port := 54321
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:], 0x0101)
	binary.BigEndian.PutUint32(msg[4:], uint32(stunMagic))
	copy(msg[8:20], []byte("0123456789ab"))

	attr := make([]byte, 12)
	binary.BigEndian.PutUint16(attr[0:], 0x0020) // XOR-MAPPED-ADDRESS
	binary.BigEndian.PutUint16(attr[2:], 8)
	attr[5] = 1 // IPv4
	m := uint32(stunMagic)
	binary.BigEndian.PutUint16(attr[6:], uint16(port)^uint16(m>>16))
	attr[8] = ip[0] ^ byte(m>>24)
	attr[9] = ip[1] ^ byte(m>>16)
	attr[10] = ip[2] ^ byte(m>>8)
	attr[11] = ip[3] ^ byte(m)
	msg = append(msg, attr...)
	binary.BigEndian.PutUint16(msg[2:], uint16(len(attr)))

	ep, txid, err := parseBinding(msg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ep.IP.Equal(net.IPv4(203, 0, 113, 7)) || ep.Port != port {
		t.Fatalf("got %v, want 203.0.113.7:%d", ep, port)
	}
	if string(txid[:]) != "0123456789ab" {
		t.Fatalf("transaction %q", txid)
	}
}

// TestParseBindingPlain reads the older MAPPED-ADDRESS form too.
func TestParseBindingPlain(t *testing.T) {
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:], 0x0101)
	binary.BigEndian.PutUint32(msg[4:], stunMagic)
	attr := []byte{0x00, 0x01, 0x00, 0x08, 0x00, 0x01, 0xd4, 0x31, 198, 51, 100, 9}
	msg = append(msg, attr...)
	binary.BigEndian.PutUint16(msg[2:], uint16(len(attr)))

	ep, _, err := parseBinding(msg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ep.IP.Equal(net.IPv4(198, 51, 100, 9)) || ep.Port != 54321 {
		t.Fatalf("got %v", ep)
	}
}

// TestParseBindingRejects: a truncated or foreign message is refused.
func TestParseBindingRejects(t *testing.T) {
	if _, _, err := parseBinding([]byte{0, 1}); err == nil {
		t.Fatal("accepted a short message")
	}
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:], 0x0111) // a request, not a response
	if _, _, err := parseBinding(msg); err == nil {
		t.Fatal("accepted a request as a response")
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct{ loc, base, control, want string }{
		{"http://192.168.1.1:5000/rootDesc.xml", "", "/ctl/IPConn", "http://192.168.1.1:5000/ctl/IPConn"},
		{"http://192.168.1.1:5000/rootDesc.xml", "http://192.168.1.1:5000", "ctl/IPConn", "http://192.168.1.1:5000/ctl/IPConn"},
		{"http://192.168.1.1:5000/rootDesc.xml", "", "http://10.0.0.1/ctl", "http://10.0.0.1/ctl"},
	}
	for _, c := range cases {
		if got := resolveURL(c.loc, c.base, c.control); got != c.want {
			t.Errorf("resolveURL(%q, %q, %q) = %q, want %q", c.loc, c.base, c.control, got, c.want)
		}
	}
}

const sampleDesc = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <URLBase>http://192.168.1.1:49152</URLBase>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/ctl/IPConn</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`

// TestWalkForWAN finds the service nested in a router's description.
func TestWalkForWAN(t *testing.T) {
	var desc igdDesc
	if err := xml.Unmarshal([]byte(sampleDesc), &desc); err != nil {
		t.Fatalf("xml: %v", err)
	}
	svc := walkForWAN(desc.Device)
	if svc == nil {
		t.Fatal("no WAN service found")
	}
	if svc.ControlURL != "/ctl/IPConn" {
		t.Fatalf("control %q", svc.ControlURL)
	}
	if got := resolveURL("http://192.168.1.1:49152/rootDesc.xml", desc.URLBase, svc.ControlURL); got != "http://192.168.1.1:49152/ctl/IPConn" {
		t.Fatalf("control url %q", got)
	}
}

func TestFaultCode(t *testing.T) {
	body := `<s:Envelope><s:Body><s:Fault><detail><UPnPError><errorCode>718</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`
	if got := faultCode(body); got != "，718" {
		t.Fatalf("fault %q", got)
	}
}
