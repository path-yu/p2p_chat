package nat

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Mapping is a port the router forwards to this host, so that peers on
// other networks can reach it.
type Mapping struct {
	// External is the address peers dial, empty when the router gave none.
	External Endpoint
	// LocalPort is the port on this host, and External.Port the one on the
	// router; they are the same unless the router refused it.
	LocalPort int
	// Protocol is "TCP" or "UDP".
	Protocol   string
	Service    string
	ControlURL string
	Lease      time.Duration

	client *http.Client
	lease  time.Duration
	stop   chan struct{}
	once   sync.Once
}

// Close takes the mapping off the router.
func (m *Mapping) Close() {
	m.once.Do(func() {
		if m.stop != nil {
			close(m.stop)
		}
		_, _ = m.soap("DeletePortMapping", fmt.Sprintf(
			"<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>TCP</NewProtocol>",
			m.External.Port))
	})
}

// Map asks the router, over UPnP, to forward a port to this host. A lease
// of zero asks for a permanent mapping.
func Map(port int, proto string, lease time.Duration) (*Mapping, error) {
	if proto == "" {
		proto = "TCP"
	}
	local, err := LocalIPv4()
	if err != nil {
		return nil, err
	}
	locations, err := searchIGD(3 * time.Second)
	if err != nil {
		return nil, err
	}
	if len(locations) == 0 {
		return nil, errors.New("upnp：局域网里没有找到路由器（未开启 UPnP 或不在同一网段）")
	}

	var lastErr error
	for _, loc := range locations {
		svc, err := findWANService(loc)
		if err != nil {
			lastErr = err
			continue
		}
		m := &Mapping{
			LocalPort:  port,
			Protocol:   proto,
			Service:    svc.typ,
			ControlURL: svc.control,
			client:     &http.Client{Timeout: 6 * time.Second},
			lease:      lease,
		}
		body := fmt.Sprintf(
			"<NewRemoteHost></NewRemoteHost>"+
				"<NewExternalPort>%d</NewExternalPort>"+
				"<NewProtocol>%s</NewProtocol>"+
				"<NewInternalPort>%d</NewInternalPort>"+
				"<NewInternalClient>%s</NewInternalClient>"+
				"<NewEnabled>1</NewEnabled>"+
				"<NewPortMappingDescription>Lumen</NewPortMappingDescription>"+
				"<NewLeaseDuration>%d</NewLeaseDuration>",
			port, proto, port, local, int(lease.Seconds()))
		if _, err := m.soap("AddPortMapping", body); err != nil {
			// 718: the port is already mapped, perhaps by an earlier run.
			if strings.Contains(err.Error(), "718") {
				_, _ = m.soap("DeletePortMapping", fmt.Sprintf(
					"<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>TCP</NewProtocol>", port))
				if _, err2 := m.soap("AddPortMapping", body); err2 == nil {
					lastErr = nil
				} else {
					lastErr = err2
					continue
				}
			} else {
				lastErr = err
				continue
			}
		}

		if ip := m.externalIP(); ip != nil {
			m.External = Endpoint{IP: ip, Port: port}
		} else {
			m.External = Endpoint{Port: port}
		}
		if lease > 0 {
			m.keep(lease)
		}
		return m, nil
	}
	if lastErr == nil {
		lastErr = errors.New("upnp：路由器没有接受端口映射")
	}
	return nil, lastErr
}

// keep renews the mapping before its lease runs out.
func (m *Mapping) keep(lease time.Duration) {
	m.stop = make(chan struct{})
	go func() {
		t := time.NewTicker(lease / 2)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				_, _ = m.soap("AddPortMapping", fmt.Sprintf(
					"<NewRemoteHost></NewRemoteHost>"+
						"<NewExternalPort>%d</NewExternalPort>"+
						"<NewProtocol>%s</NewProtocol>"+
						"<NewInternalPort>%d</NewInternalPort>"+
						"<NewInternalClient>%s</NewInternalClient>"+
						"<NewEnabled>1</NewEnabled>"+
						"<NewPortMappingDescription>Lumen</NewPortMappingDescription>"+
						"<NewLeaseDuration>%d</NewLeaseDuration>",
					m.External.Port, m.Protocol, m.LocalPort, m.local(), int(lease.Seconds())))
			}
		}
	}()
}

func (m *Mapping) local() string {
	ip, err := LocalIPv4()
	if err != nil {
		return "0.0.0.0"
	}
	return ip
}

// externalIP asks the router which address it holds on the internet.
func (m *Mapping) externalIP() net.IP {
	body, err := m.soap("GetExternalIPAddress", "")
	if err != nil {
		return nil
	}
	start := strings.Index(body, "<NewExternalIPAddress>")
	end := strings.Index(body, "</NewExternalIPAddress>")
	if start < 0 || end < start {
		return nil
	}
	ip := net.ParseIP(strings.TrimSpace(body[start+len("<NewExternalIPAddress>") : end]))
	if ip == nil || ip.IsUnspecified() || ip.IsPrivate() {
		return nil
	}
	return ip
}

func (m *Mapping) soap(action, args string) (string, error) {
	if m.ControlURL == "" {
		return "", errors.New("upnp：没有控制地址")
	}
	env := `<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + m.Service + `">` + args + `</u:` + action +
		`></s:Body></s:Envelope>`

	req, err := http.NewRequest("POST", m.ControlURL, bytes.NewReader([]byte(env)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf("%q", m.Service+"#"+action))

	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("upnp：%s 返回 %s%s", action, resp.Status, faultCode(string(data)))
	}
	return string(data), nil
}

// faultCode pulls the error code out of a SOAP fault, for the log.
func faultCode(body string) string {
	i := strings.Index(body, "<errorCode>")
	j := strings.Index(body, "</errorCode>")
	if i < 0 || j < i {
		return ""
	}
	return "，" + strings.TrimSpace(body[i+len("<errorCode>"):j])
}

// searchIGD asks the network, over SSDP, where the router's description is.
func searchIGD(timeout time.Duration) ([]string, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	targets := []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	}
	dest := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, st := range targets {
		msg := "M-SEARCH * HTTP/1.1\r\n" +
			"HOST: 239.255.255.250:1900\r\n" +
			"MAN: \"ssdp:discover\"\r\n" +
			"MX: 2\r\n" +
			"ST: " + st + "\r\n\r\n"
		for i := 0; i < 2; i++ {
			_, _ = conn.WriteToUDP([]byte(msg), dest)
		}
	}

	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	seen := map[string]bool{}
	var out []string
	for time.Now().Before(deadline) {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		loc := headerValue(string(buf[:n]), "location")
		if loc == "" || seen[loc] {
			continue
		}
		seen[loc] = true
		out = append(out, loc)
	}
	return out, nil
}

func headerValue(msg, key string) string {
	sc := bufio.NewScanner(strings.NewReader(msg))
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(line[:i]), key) {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

type igdDesc struct {
	URLBase string    `xml:"URLBase"`
	Device  igdDevice `xml:"device"`
}

type igdDevice struct {
	DeviceType string      `xml:"deviceType"`
	Devices    []igdDevice `xml:"deviceList>device"`
	Services   []igdSvc    `xml:"serviceList>service"`
}

type igdSvc struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
	typ         string
	control     string
}

// findWANService reads a router's description and finds the service that
// opens ports on its internet side.
func findWANService(location string) (*igdSvc, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(location)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var desc igdDesc
	if err := xml.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("upnp：描述文件无法解析: %w", err)
	}
	if svc := walkForWAN(desc.Device); svc != nil {
		svc.control = resolveURL(location, desc.URLBase, svc.ControlURL)
		return svc, nil
	}
	return nil, errors.New("upnp：路由器没有 WAN 连接服务")
}

func walkForWAN(d igdDevice) *igdSvc {
	for i := range d.Services {
		t := d.Services[i].ServiceType
		if strings.Contains(t, "WANIPConnection") || strings.Contains(t, "WANPPPConnection") {
			s := d.Services[i]
			s.typ = t
			return &s
		}
	}
	for i := range d.Devices {
		if s := walkForWAN(d.Devices[i]); s != nil {
			return s
		}
	}
	return nil
}

// resolveURL turns a control path into a full URL, against the base the
// description gives or the location it came from.
func resolveURL(location, base, control string) string {
	if strings.HasPrefix(control, "http://") || strings.HasPrefix(control, "https://") {
		return control
	}
	if base != "" && strings.HasPrefix(base, "http") {
		return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(control, "/")
	}
	i := strings.Index(location, "://")
	j := strings.Index(location[i+3:], "/")
	if i < 0 || j < 0 {
		return location
	}
	return location[:i+3+j] + "/" + strings.TrimPrefix(control, "/")
}

// LocalIPv4 is this host's address on the network it reaches the internet
// through.
func LocalIPv4() (string, error) {
	c, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer c.Close()
	if ua, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return ua.IP.String(), nil
	}
	return "", errors.New("没有找到本机地址")
}
