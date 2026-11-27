package goed2k

import (
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type LinkType string

const (
	LinkServer  LinkType = "SERVER"
	LinkServers LinkType = "SERVERS"
	LinkNodes   LinkType = "NODES"
	LinkFile    LinkType = "FILE"
)

type EMuleLink struct {
	Hash        protocol.Hash
	NumberValue int64
	StringValue string
	Type        LinkType
	// AICHRoot holds the 20-byte SHA-1 master hash from the link's h= field (#4).
	AICHRoot []byte
}

func ParseEMuleLink(uri string) (EMuleLink, error) {
	if uri == "" {
		return EMuleLink{}, NewError(LinkMailformed)
	}

	decURI, err := url.QueryUnescape(uri)
	if err != nil {
		return EMuleLink{}, NewError(UnsupportedEncoding)
	}

	parts := strings.Split(decURI, "|")
	if len(parts) < 2 || parts[0] != "ed2k://" || parts[len(parts)-1] != "/" {
		return EMuleLink{}, NewError(LinkMailformed)
	}

	switch parts[1] {
	case "server":
		if len(parts) != 5 {
			return EMuleLink{}, NewError(LinkMailformed)
		}
		port, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return EMuleLink{}, NewError(NumberFormatError)
		}
		return EMuleLink{NumberValue: port, StringValue: parts[2], Type: LinkServer}, nil
	case "serverlist":
		if len(parts) != 4 {
			return EMuleLink{}, NewError(LinkMailformed)
		}
		return EMuleLink{StringValue: parts[2], Type: LinkServers}, nil
	case "nodeslist":
		if len(parts) != 4 {
			return EMuleLink{}, NewError(LinkMailformed)
		}
		return EMuleLink{StringValue: parts[2], Type: LinkNodes}, nil
	case "file":
		if len(parts) < 6 {
			return EMuleLink{}, NewError(LinkMailformed)
		}
		hash, err := protocol.HashFromString(parts[4])
		if err != nil {
			return EMuleLink{}, NewError(InternalError)
		}
		size, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return EMuleLink{}, NewError(NumberFormatError)
		}
		name, err := url.QueryUnescape(parts[2])
		if err != nil {
			return EMuleLink{}, NewError(UnsupportedEncoding)
		}
		link := EMuleLink{
			Hash:        hash,
			NumberValue: size,
			StringValue: name,
			Type:        LinkFile,
		}
		// #4: h=<32 base32> is the AICH SHA-1 master hash used for corruption
		// recovery (ED2KLink.cpp:370 decodes it with DecodeBase32). A 40 hex
		// value is also accepted because some generators emit that form.
		// Malformed values are ignored, not fatal to the link.
		for _, field := range parts[5 : len(parts)-1] {
			if !strings.HasPrefix(field, "h=") {
				continue
			}
			raw := field[2:]
			switch {
			case len(raw) == 32:
				if root, err := decodeBase32(raw); err == nil && len(root) == 20 {
					link.AICHRoot = root
				}
			case len(raw) == 40:
				if root, err := hex.DecodeString(raw); err == nil && len(root) == 20 {
					link.AICHRoot = root
				}
			}
			break
		}
		return link, nil
	}

	return EMuleLink{}, NewError(UnknownLinkType)
}
