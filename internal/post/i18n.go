package post

import "strings"

// The portal's words in Greek, Dutch and English. Plain language, short
// lines: the page is read on a phone, under stress, often by somebody who
// did not choose the language. A key missing from a language falls back to
// English.

// Languages the portal speaks, in the order the switch shows them.
var Languages = []string{"el", "nl", "en"}

// DefaultLanguage is used when nothing in the request chooses one.
const DefaultLanguage = "en"

var strs = map[string]map[string]string{
	"en": {
		"lang_name":      "English",
		"title":          "Community Post",
		"nav_write":      "Write",
		"nav_pickup":     "Pick up",
		"nav_board":      "Safe and well",
		"nav_about":      "About this post",
		"write_intro":    "Write a short message to a phone number or an email address. It leaves over satellite. The answer comes back here: keep your pickup code.",
		"recipient":      "To (phone number with country code, or email)",
		"recipient_hint": "+31 6 12345678 or name@example.org",
		"name":           "Your name (optional)",
		"message":        "Message",
		"chars_left":     "at most %d characters, no links",
		"emergency":      "This is an emergency",
		"emergency_hint": "An emergency goes first and is marked SOS for the person who receives it.",
		"send":           "Send",
		"sent_title":     "Your pickup code",
		"sent_intro":     "Take a screenshot of this code. The reply is collected with it. Nothing else is kept on your phone.",
		"sent_pending":   "An operator at this post checks every message before it leaves. Come back later with your code.",
		"sent_queued":    "Your message is on its way. A satellite link can take minutes to hours. Come back later with your code.",
		"sent_to":        "To",
		"pickup_title":   "Pick up a reply",
		"pickup_intro":   "Enter the code you were given.",
		"code":           "Pickup code",
		"lookup":         "Look up",
		"status":         "Status",
		"status_pending": "waiting for the operator",
		"status_queued":  "leaving the post",
		"status_sent":    "sent, no reply yet",
		"status_replied": "reply received",
		"status_refused": "not sent (refused by the operator)",
		"status_failed":  "not sent (the link did not take it)",
		"replies":        "Replies",
		"no_replies":     "No reply yet. Check again later.",
		"unknown_code":   "That code is not known at this post. Check it against your screenshot.",
		"your_posts":     "Your messages from this phone",
		"board_title":    "Safe and well",
		"board_intro":    "A board people can search by name. Putting your name on it is your choice; it stays %d days and is not sent anywhere.",
		"board_name":     "Name",
		"board_status":   "Status (for example: safe, at the school, looking for)",
		"board_place":    "Place (optional)",
		"board_add":      "Put me on the board",
		"board_search":   "Search by name",
		"board_find":     "Find",
		"board_empty":    "Nobody by that name on the board.",
		"board_added":    "You are on the board.",
		"about_title":    "About this post",
		"about_operator": "This post is run by",
		"about_how":      "A message you write here leaves this post over a satellite link to the MeshSat Hub, which sends it as an SMS or an email from the operator's account. The person's reply comes back the same way and waits here under your pickup code.",
		"about_cost":     "Every message and every reply costs the operator satellite credit. Keep it short, and do not send the same message twice.",
		"data_title":     "What is kept",
		"data_notice":    "Kept on this post: your message, the address you wrote to, the name you gave, the reply, and a counter per phone so one phone cannot use everything. Kept until the operator closes the post, at most 30 days. Sent onward: only the message, your name and the pickup code. The safe-and-well board is separate and only holds what you put on it yourself. No account, no app, no tracking. The legal ground is the protection of your or another person's vital interests; the operator named above is responsible for the data.",
		"done":           "I am done",
		"done_title":     "Done",
		"done_text":      "You can close this page. To read a reply later, join this WiFi again and open any web page.",
		"err_recipient":  "Enter a phone number with its country code (for example +31 6 12345678) or an email address.",
		"err_empty":      "Write a message first.",
		"err_too_long":   "The message is too long.",
		"err_link":       "Links cannot be sent from this post. Write it in words.",
		"err_control":    "The message holds a character this post cannot send.",
		"err_name":       "The name is too long or holds a character this post cannot send (no colon, no brackets).",
		"err_quota":      "This phone has sent its share for now. Try again later, or ask the operator.",
		"err_other":      "Something went wrong at the post. Tell the operator.",
		"language":       "Language",
		"powered":        "MeshSat Community Post",
	},
	"nl": {
		"lang_name":      "Nederlands",
		"title":          "Berichtenpost",
		"nav_write":      "Schrijven",
		"nav_pickup":     "Ophalen",
		"nav_board":      "Veilig en wel",
		"nav_about":      "Over deze post",
		"write_intro":    "Schrijf een kort bericht naar een telefoonnummer of een e-mailadres. Het gaat via satelliet. Het antwoord komt hier terug: bewaar je ophaalcode.",
		"recipient":      "Aan (telefoonnummer met landcode, of e-mail)",
		"recipient_hint": "+31 6 12345678 of naam@voorbeeld.nl",
		"name":           "Je naam (niet verplicht)",
		"message":        "Bericht",
		"chars_left":     "hoogstens %d tekens, geen links",
		"emergency":      "Dit is een noodgeval",
		"emergency_hint": "Een noodgeval gaat voor en wordt als SOS gemarkeerd voor de ontvanger.",
		"send":           "Versturen",
		"sent_title":     "Je ophaalcode",
		"sent_intro":     "Maak een schermafbeelding van deze code. Het antwoord haal je ermee op. Verder wordt niets op je telefoon bewaard.",
		"sent_pending":   "Een medewerker van deze post bekijkt elk bericht voordat het weggaat. Kom later terug met je code.",
		"sent_queued":    "Je bericht is onderweg. Een satellietverbinding kan minuten tot uren duren. Kom later terug met je code.",
		"sent_to":        "Aan",
		"pickup_title":   "Antwoord ophalen",
		"pickup_intro":   "Vul de code in die je hebt gekregen.",
		"code":           "Ophaalcode",
		"lookup":         "Opzoeken",
		"status":         "Status",
		"status_pending": "wacht op de medewerker",
		"status_queued":  "verlaat de post",
		"status_sent":    "verzonden, nog geen antwoord",
		"status_replied": "antwoord ontvangen",
		"status_refused": "niet verzonden (geweigerd door de medewerker)",
		"status_failed":  "niet verzonden (de verbinding nam het niet aan)",
		"replies":        "Antwoorden",
		"no_replies":     "Nog geen antwoord. Kijk later nog eens.",
		"unknown_code":   "Die code is bij deze post niet bekend. Vergelijk met je schermafbeelding.",
		"your_posts":     "Je berichten vanaf deze telefoon",
		"board_title":    "Veilig en wel",
		"board_intro":    "Een bord dat mensen op naam kunnen doorzoeken. Je naam erop zetten is je eigen keuze; het blijft %d dagen staan en wordt nergens heen gestuurd.",
		"board_name":     "Naam",
		"board_status":   "Status (bijvoorbeeld: veilig, bij de school, op zoek naar)",
		"board_place":    "Plaats (niet verplicht)",
		"board_add":      "Zet mij op het bord",
		"board_search":   "Zoek op naam",
		"board_find":     "Zoeken",
		"board_empty":    "Niemand met die naam op het bord.",
		"board_added":    "Je staat op het bord.",
		"about_title":    "Over deze post",
		"about_operator": "Deze post wordt gerund door",
		"about_how":      "Een bericht dat je hier schrijft verlaat deze post via een satellietverbinding naar de MeshSat Hub, die het als sms of e-mail verstuurt vanaf het account van de beheerder. Het antwoord komt langs dezelfde weg terug en wacht hier onder je ophaalcode.",
		"about_cost":     "Elk bericht en elk antwoord kost de beheerder satelliettegoed. Houd het kort en stuur hetzelfde bericht niet twee keer.",
		"data_title":     "Wat wordt bewaard",
		"data_notice":    "Bewaard op deze post: je bericht, het adres waar je naartoe schreef, de naam die je opgaf, het antwoord, en een teller per telefoon zodat een telefoon niet alles kan opmaken. Bewaard tot de beheerder de post sluit, hoogstens 30 dagen. Doorgestuurd: alleen het bericht, je naam en de ophaalcode. Het bord Veilig en wel staat los hiervan en bevat alleen wat je er zelf op zet. Geen account, geen app, geen tracking. De grondslag is de bescherming van jouw vitale belangen of die van een ander; de hierboven genoemde beheerder is verantwoordelijk voor de gegevens.",
		"done":           "Ik ben klaar",
		"done_title":     "Klaar",
		"done_text":      "Je kunt deze pagina sluiten. Om later een antwoord te lezen: verbind weer met deze wifi en open een willekeurige webpagina.",
		"err_recipient":  "Vul een telefoonnummer met landcode in (bijvoorbeeld +31 6 12345678) of een e-mailadres.",
		"err_empty":      "Schrijf eerst een bericht.",
		"err_too_long":   "Het bericht is te lang.",
		"err_link":       "Links kunnen vanaf deze post niet worden verstuurd. Schrijf het in woorden.",
		"err_control":    "Het bericht bevat een teken dat deze post niet kan versturen.",
		"err_name":       "De naam is te lang of bevat een teken dat deze post niet kan versturen (geen dubbele punt, geen haakjes).",
		"err_quota":      "Deze telefoon heeft voorlopig genoeg verstuurd. Probeer het later, of vraag de medewerker.",
		"err_other":      "Er ging iets mis op de post. Zeg het tegen de medewerker.",
		"language":       "Taal",
		"powered":        "MeshSat Berichtenpost",
	},
	"el": {
		"lang_name":      "Ελληνικά",
		"title":          "Κοινοτικό Ταχυδρομείο",
		"nav_write":      "Γράψε",
		"nav_pickup":     "Παραλαβή",
		"nav_board":      "Είμαι καλά",
		"nav_about":      "Για αυτό το σημείο",
		"write_intro":    "Γράψε ένα σύντομο μήνυμα σε έναν αριθμό τηλεφώνου ή μια διεύθυνση email. Φεύγει μέσω δορυφόρου. Η απάντηση έρχεται εδώ: κράτα τον κωδικό παραλαβής σου.",
		"recipient":      "Προς (αριθμός τηλεφώνου με κωδικό χώρας, ή email)",
		"recipient_hint": "+30 69 12345678 ή onoma@example.gr",
		"name":           "Το όνομά σου (προαιρετικό)",
		"message":        "Μήνυμα",
		"chars_left":     "έως %d χαρακτήρες, χωρίς συνδέσμους",
		"emergency":      "Είναι έκτακτη ανάγκη",
		"emergency_hint": "Μια έκτακτη ανάγκη προηγείται και σημαίνεται ως SOS για όποιον τη λάβει.",
		"send":           "Αποστολή",
		"sent_title":     "Ο κωδικός παραλαβής σου",
		"sent_intro":     "Βγάλε στιγμιότυπο οθόνης αυτού του κωδικού. Με αυτόν παραλαμβάνεις την απάντηση. Τίποτα άλλο δεν μένει στο τηλέφωνό σου.",
		"sent_pending":   "Ένας χειριστής σε αυτό το σημείο ελέγχει κάθε μήνυμα πριν φύγει. Έλα αργότερα με τον κωδικό σου.",
		"sent_queued":    "Το μήνυμά σου είναι καθ' οδόν. Μια δορυφορική ζεύξη μπορεί να πάρει λεπτά έως ώρες. Έλα αργότερα με τον κωδικό σου.",
		"sent_to":        "Προς",
		"pickup_title":   "Παραλαβή απάντησης",
		"pickup_intro":   "Γράψε τον κωδικό που σου δόθηκε.",
		"code":           "Κωδικός παραλαβής",
		"lookup":         "Αναζήτηση",
		"status":         "Κατάσταση",
		"status_pending": "περιμένει τον χειριστή",
		"status_queued":  "φεύγει από το σημείο",
		"status_sent":    "στάλθηκε, χωρίς απάντηση ακόμα",
		"status_replied": "ήρθε απάντηση",
		"status_refused": "δεν στάλθηκε (απορρίφθηκε από τον χειριστή)",
		"status_failed":  "δεν στάλθηκε (η ζεύξη δεν το δέχτηκε)",
		"replies":        "Απαντήσεις",
		"no_replies":     "Καμία απάντηση ακόμα. Ξαναδοκίμασε αργότερα.",
		"unknown_code":   "Αυτός ο κωδικός δεν είναι γνωστός σε αυτό το σημείο. Σύγκρινέ τον με το στιγμιότυπό σου.",
		"your_posts":     "Τα μηνύματά σου από αυτό το τηλέφωνο",
		"board_title":    "Είμαι καλά",
		"board_intro":    "Ένας πίνακας που ψάχνει κανείς με όνομα. Το αν θα βάλεις το όνομά σου είναι δική σου επιλογή· μένει %d ημέρες και δεν στέλνεται πουθενά.",
		"board_name":     "Όνομα",
		"board_status":   "Κατάσταση (π.χ.: καλά, στο σχολείο, ψάχνω τον/την)",
		"board_place":    "Τόπος (προαιρετικό)",
		"board_add":      "Βάλε με στον πίνακα",
		"board_search":   "Αναζήτηση με όνομα",
		"board_find":     "Εύρεση",
		"board_empty":    "Κανείς με αυτό το όνομα στον πίνακα.",
		"board_added":    "Είσαι στον πίνακα.",
		"about_title":    "Για αυτό το σημείο",
		"about_operator": "Αυτό το σημείο το λειτουργεί",
		"about_how":      "Ένα μήνυμα που γράφεις εδώ φεύγει από αυτό το σημείο μέσω δορυφορικής ζεύξης προς το MeshSat Hub, που το στέλνει ως SMS ή email από τον λογαριασμό του διαχειριστή. Η απάντηση γυρίζει από τον ίδιο δρόμο και περιμένει εδώ με τον κωδικό παραλαβής σου.",
		"about_cost":     "Κάθε μήνυμα και κάθε απάντηση κοστίζει στον διαχειριστή δορυφορικές μονάδες. Γράψε σύντομα και μη στείλεις το ίδιο μήνυμα δύο φορές.",
		"data_title":     "Τι φυλάσσεται",
		"data_notice":    "Φυλάσσονται σε αυτό το σημείο: το μήνυμά σου, η διεύθυνση στην οποία έγραψες, το όνομα που έδωσες, η απάντηση, και ένας μετρητής ανά τηλέφωνο ώστε να μην εξαντλεί ένα τηλέφωνο τα πάντα. Μένουν μέχρι ο διαχειριστής να κλείσει το σημείο, το πολύ 30 ημέρες. Προωθούνται: μόνο το μήνυμα, το όνομά σου και ο κωδικός παραλαβής. Ο πίνακας Είμαι καλά είναι ξεχωριστός και περιέχει μόνο ό,τι βάζεις εσύ. Χωρίς λογαριασμό, χωρίς εφαρμογή, χωρίς παρακολούθηση. Η νομική βάση είναι η προστασία ζωτικών συμφερόντων, δικών σου ή άλλου· υπεύθυνος για τα δεδομένα είναι ο διαχειριστής που αναφέρεται παραπάνω.",
		"done":           "Τελείωσα",
		"done_title":     "Έτοιμο",
		"done_text":      "Μπορείς να κλείσεις αυτή τη σελίδα. Για να διαβάσεις αργότερα μια απάντηση, συνδέσου ξανά σε αυτό το WiFi και άνοιξε οποιαδήποτε ιστοσελίδα.",
		"err_recipient":  "Γράψε έναν αριθμό τηλεφώνου με κωδικό χώρας (π.χ. +30 69 12345678) ή μια διεύθυνση email.",
		"err_empty":      "Γράψε πρώτα ένα μήνυμα.",
		"err_too_long":   "Το μήνυμα είναι πολύ μεγάλο.",
		"err_link":       "Σύνδεσμοι δεν στέλνονται από αυτό το σημείο. Γράψ' το με λόγια.",
		"err_control":    "Το μήνυμα περιέχει χαρακτήρα που αυτό το σημείο δεν μπορεί να στείλει.",
		"err_name":       "Το όνομα είναι πολύ μεγάλο ή περιέχει χαρακτήρα που δεν στέλνεται (όχι άνω-κάτω τελεία, όχι αγκύλες).",
		"err_quota":      "Αυτό το τηλέφωνο έστειλε το μερίδιό του προς το παρόν. Δοκίμασε αργότερα ή ρώτα τον χειριστή.",
		"err_other":      "Κάτι πήγε στραβά στο σημείο. Πες το στον χειριστή.",
		"language":       "Γλώσσα",
		"powered":        "MeshSat Κοινοτικό Ταχυδρομείο",
	},
}

// T is one word or sentence in lang, English when the language lacks it,
// the key when nobody has it.
func T(lang, key string) string {
	if m, ok := strs[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := strs[DefaultLanguage][key]; ok {
		return v
	}
	return key
}

// KnownLanguage reports whether the portal speaks lang.
func KnownLanguage(lang string) bool {
	_, ok := strs[lang]
	return ok
}

// PickLanguage chooses from an Accept-Language header: the first language
// the portal speaks, in the header's order; DefaultLanguage otherwise. The
// q-values are not weighed, the order is enough for a phone.
func PickLanguage(accept string) string {
	for _, part := range strings.Split(accept, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if tag == "" {
			continue
		}
		base := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		if KnownLanguage(base) {
			return base
		}
	}
	return DefaultLanguage
}
