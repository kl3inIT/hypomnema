package serviceproxy

// WhatsAppID is the WhatsApp Integration's id (integration.WhatsAppID).
const WhatsAppID = "whatsapp"

// whatsappCatalog is what a Bot does with the owner's WhatsApp (issue #645): its acts, not HTTP requests, as the
// control plane holds the session (act.go). It reaches no host. Sending as the owner needs care, so it asks until
// he sets another answer (Gmail's gmail.send does the same).
func whatsappCatalog() Catalog {
	return Catalog{Integration: WhatsAppID, Actions: []Action{{
		ID: "whatsapp.send", Service: "WhatsApp", Name: "Send a message as you",
		Text: "A message or a file sent in one of your chats, in your name", Kind: NeedsCare,
	}}}
}

// ZaloID is the Zalo Integration's id (integration.ZaloID).
const ZaloID = "zalo"

// zaloCatalog is what a Bot does with the owner's Zalo (issue #680), as WhatsApp's: sending as him asks until he
// sets another answer.
func zaloCatalog() Catalog {
	return Catalog{Integration: ZaloID, Actions: []Action{{
		ID: "zalo.send", Service: "Zalo", Name: "Send a message as you",
		Text: "A message or a file sent in one of your chats, in your name", Kind: NeedsCare,
	}}}
}
