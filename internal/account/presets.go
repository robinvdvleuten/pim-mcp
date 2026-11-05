package account

// preset holds the settings of a provider whose servers are known. Self-hosted
// servers need none, because they answer at their well-known URL.
type preset struct {
	imap   IMAP
	caldav CalDAV
}

var (
	mailboxOrg = preset{
		imap:   IMAP{Addr: "imap.mailbox.org:993", Security: SecurityTLS},
		caldav: CalDAV{URL: "https://dav.mailbox.org/"},
	}
	fastmail = preset{
		imap:   IMAP{Addr: "imap.fastmail.com:993", Security: SecurityTLS},
		caldav: CalDAV{URL: "https://caldav.fastmail.com/"},
	}
	posteo = preset{
		imap:   IMAP{Addr: "posteo.de:993", Security: SecurityTLS},
		caldav: CalDAV{URL: "https://posteo.de:8443/"},
	}
)

// presets maps email domains to providers. Customers' own domains aren't here,
// and are found through DNS instead.
var presets = map[string]preset{
	"mailbox.org":  mailboxOrg,
	"fastmail.com": fastmail,
	"fastmail.fm":  fastmail,
	"posteo.de":    posteo,
	"posteo.net":   posteo,
	"posteo.org":   posteo,
	"posteo.eu":    posteo,
	"posteo.at":    posteo,
	"posteo.ch":    posteo,
	"posteo.com":   posteo,
}
