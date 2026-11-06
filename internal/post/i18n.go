package post

import (
	"fmt"
	"strings"
	"time"
)

// The portal's words in Greek, Dutch, German and English. Plain language, short
// lines: the page is read on a phone, under stress, often by somebody who
// did not choose the language. A key missing from a language falls back to
// English.
//
// Register: German "Sie", Dutch "je" (as NL-Alert and the Rijksoverheid
// address residents), Greek the polite plural "εσείς" (as the 112 alerts and
// gov.gr do). The person at the kit who checks the messages is the
// volunteer ("Team", "medewerker", "εθελοντής", the words the site uses);
// the organisation that runs the post is the operator ("Betreiber",
// "beheerder", "φορέας").
//
// Counts go through Plural: a key with a count has a "_one" and an "_other"
// form (English, German, Dutch and Greek share that rule for whole numbers).
// The day counts in board_intro and data_notice are such a phrase, put in
// with %s ("30 days", "1 day").

// Languages the portal speaks, in the order the switch shows them.
var Languages = []string{"el", "nl", "de", "en"}

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
		"emergency_hint": "An emergency goes first and is marked SOS for the person who receives it. It does not call 112.",
		"send":           "Send",
		"sent_title":     "Your pickup code",
		"sent_intro":     "Take a screenshot of this code. The reply is collected with it. This page leaves only two cookies on your phone: your language, and for one day which messages are yours.",
		"sent_pending":   "A volunteer at this post checks every message before it leaves. Come back later with your code.",
		"sent_queued":    "Your message is on its way. A satellite link can take minutes to hours. Come back later with your code.",
		"sent_to":        "To",
		"pickup_title":   "Pick up a reply",
		"pickup_intro":   "Enter the code you were given.",
		"code":           "Pickup code",
		"lookup":         "Look up",
		"status":         "Status",
		"status_pending": "waiting for the volunteer",
		"status_queued":  "waiting for the satellite",
		"status_sent":    "sent, no reply yet",
		"status_replied": "reply received",
		"status_refused": "not sent (refused by the volunteer)",
		"status_failed":  "not sent (sending failed)",
		"replies":        "Replies",
		"no_replies":     "No reply yet. Check again later.",
		"unknown_code":   "That code is not known at this post. Check it against your screenshot.",
		"your_posts":     "Your messages from this phone",
		"board_title":    "Safe and well",
		"board_intro":    "A board people can search by name. Putting your name on it is your choice; it stays %s and is not sent anywhere.",
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
		"data_notice":    "Kept on this post: your message, the address you wrote to, the name you gave, the reply, and a counter per phone so one phone cannot use everything. Deleted %s after the operator closes the post, or sooner by the operator; the kit's send log keeps the sent text under its own retention. Sent onward: only the address you wrote to, the message, your name and the pickup code. The safe-and-well board is separate, holds only what you put on it yourself, and a line stays %s. On your phone: two cookies, your language for 30 days and which messages are yours for one day. No account, no app, no tracking. The legal ground is the protection of your or another person's vital interests; the operator named above is responsible for the data.",
		"done":           "I am done",
		"done_title":     "Done",
		"done_text":      "You can close this page. To read a reply later, join this WiFi again and open any web page.",
		"closed_title":   "This post is closed",
		"closed_text":    "No new message can be written here. A reply can still be picked up with your code until %s; after that everything is deleted.",
		"err_recipient":  "Enter a phone number with its country code (for example +31 6 12345678) or an email address.",
		"err_empty":      "Write a message first.",
		"err_too_long":   "The message is too long.",
		"err_link":       "Links cannot be sent from this post. Write it in words.",
		"err_control":    "The message holds a character this post cannot send.",
		"err_name":       "The name is too long or holds a character this post cannot send (no colon, no @, no square brackets).",
		"err_quota":      "This phone has sent its share for now. Try again later, or ask the volunteer.",
		"err_other":      "Something went wrong at the post. Tell the volunteer.",
		"language":       "Language",
		"powered":        "MeshSat Community Post",
		"warn_title":     "Public warning",
		"warn_test":      "TEST",
		"warn_update":    "Update",
		"warn_until":     "until",
		// Added for MESHSAT-1803.
		"days_one":                "%d day",
		"days_other":              "%d days",
		"chars_remaining_one":     "%d character left, no links",
		"chars_remaining_other":   "%d characters left, no links",
		"err_too_long_by_one":     "The message is %d character too long.",
		"err_too_long_by_other":   "The message is %d characters too long.",
		"reply_hub_not_sent":      "Not sent. Please tell the volunteer. Reason from the MeshSat Hub: %s",
		"operator_not_set":        "(not set)",
		"warn_headline":           "%s %s",
		"warn_label":              "Relay of %s warning, issued %s via MeshSat, not an official NL-Alert",
		"warn_src_dwd":            "DWD",
		"warn_src_meteoalarm_nl":  "KNMI via MeteoAlarm",
		"warn_src_test":           "MeshSat test",
		"warn_src_unknown":        "unknown source",
		"warn_event_other":        "Other",
		"warn_event_wind":         "Wind",
		"warn_event_rain":         "Rain",
		"warn_event_snow_ice":     "Snow/ice",
		"warn_event_thunderstorm": "Thunderstorm",
		"warn_event_fog":          "Fog",
		"warn_event_heat":         "Heat",
		"warn_event_cold":         "Cold",
		"warn_event_coastal":      "Coastal",
		"warn_event_flood":        "Flood",
		"warn_event_avalanche":    "Avalanche",
		"warn_event_wildfire":     "Wildfire",
		"warn_event_tornado":      "Tornado",
		"warn_sev_unknown":        "Unknown",
		"warn_sev_minor":          "Minor",
		"warn_sev_moderate":       "Moderate",
		"warn_sev_severe":         "Severe",
		"warn_sev_extreme":        "Extreme",
	},
	"nl": {
		"lang_name":      "Nederlands",
		"title":          "Berichtenpunt",
		"nav_write":      "Schrijven",
		"nav_pickup":     "Ophalen",
		"nav_board":      "Ik ben veilig",
		"nav_about":      "Over dit punt",
		"write_intro":    "Schrijf een kort bericht naar een telefoonnummer of een e-mailadres. Het gaat via satelliet. Het antwoord komt hier terug: bewaar je ophaalcode.",
		"recipient":      "Aan (telefoonnummer met landnummer, of e-mail)",
		"recipient_hint": "+31 6 12345678 of naam@voorbeeld.nl",
		"name":           "Je naam (niet verplicht)",
		"message":        "Bericht",
		"chars_left":     "hoogstens %d tekens, geen links",
		"emergency":      "Dit is een noodgeval",
		"emergency_hint": "Een noodgeval gaat voor en de ontvanger ziet het als SOS gemarkeerd. Dit is geen 112-melding.",
		"send":           "Versturen",
		"sent_title":     "Je ophaalcode",
		"sent_intro":     "Maak een schermafbeelding van deze code. Het antwoord haal je ermee op. Deze pagina zet alleen twee cookies op je telefoon: je taal en, voor één dag, welke berichten van jou zijn.",
		"sent_pending":   "Een medewerker van dit punt bekijkt elk bericht voordat het weggaat. Kom later terug met je code.",
		"sent_queued":    "Je bericht is onderweg. Via de satelliet kan dat minuten tot uren duren. Kom later terug met je code.",
		"sent_to":        "Aan",
		"pickup_title":   "Antwoord ophalen",
		"pickup_intro":   "Vul de code in die je hebt gekregen.",
		"code":           "Ophaalcode",
		"lookup":         "Opzoeken",
		"status":         "Status",
		"status_pending": "wacht op de medewerker",
		"status_queued":  "wacht op de satelliet",
		"status_sent":    "verstuurd, nog geen antwoord",
		"status_replied": "antwoord ontvangen",
		"status_refused": "niet verstuurd (geweigerd door de medewerker)",
		"status_failed":  "niet verstuurd (versturen mislukt)",
		"replies":        "Antwoorden",
		"no_replies":     "Nog geen antwoord. Kijk later nog eens.",
		"unknown_code":   "Die code is bij dit punt niet bekend. Vergelijk hem met je schermafbeelding.",
		"your_posts":     "Je berichten vanaf deze telefoon",
		"board_title":    "Ik ben veilig",
		"board_intro":    "Een lijst die mensen op naam kunnen doorzoeken. Of je je naam erop zet, kies je zelf; je naam blijft %s staan en wordt nergens heen gestuurd.",
		"board_name":     "Naam",
		"board_status":   "Status (bijvoorbeeld: veilig, bij de school, op zoek naar)",
		"board_place":    "Plaats (niet verplicht)",
		"board_add":      "Zet me op de lijst",
		"board_search":   "Zoek op naam",
		"board_find":     "Zoeken",
		"board_empty":    "Niemand met die naam op de lijst.",
		"board_added":    "Je staat op de lijst.",
		"about_title":    "Over dit punt",
		"about_operator": "Dit punt wordt beheerd door",
		"about_how":      "Een bericht dat je hier schrijft verlaat dit punt via een satellietverbinding naar de MeshSat Hub, die het als sms of e-mail verstuurt vanaf het account van de beheerder. Het antwoord komt langs dezelfde weg terug en wacht hier onder je ophaalcode.",
		"about_cost":     "Elk bericht en elk antwoord kost de beheerder satelliettegoed. Houd het kort en stuur hetzelfde bericht niet twee keer.",
		"data_title":     "Wat wordt bewaard",
		"data_notice":    "Bewaard op dit punt: je bericht, het adres waar je naartoe schreef, de naam die je opgaf, het antwoord, en een teller per telefoon zodat één telefoon niet alles kan opmaken. Gewist %s nadat de beheerder het punt sluit, of eerder door de beheerder; het verzendlogboek van de kit bewaart de verzonden tekst volgens zijn eigen termijn. Doorgestuurd: alleen het adres, het bericht, je naam en de ophaalcode. De lijst ‘Ik ben veilig’ staat los hiervan, bevat alleen wat je er zelf op zet, en een regel blijft %s staan. Op je telefoon: twee cookies, je taal voor 30 dagen en voor één dag welke berichten van jou zijn. Geen account, geen app, geen tracking. De grondslag is de bescherming van jouw vitale belangen of die van een ander; de hierboven genoemde beheerder is verantwoordelijk voor de gegevens.",
		"done":           "Klaar",
		"done_title":     "Klaar",
		"done_text":      "Je kunt deze pagina sluiten. Wil je later een antwoord lezen? Maak dan weer verbinding met deze wifi en open een willekeurige webpagina.",
		"closed_title":   "Dit punt is gesloten",
		"closed_text":    "Hier kan geen nieuw bericht meer worden geschreven. Een antwoord kun je met je code nog ophalen tot %s; daarna wordt alles gewist.",
		"err_recipient":  "Vul een telefoonnummer met landnummer (bijvoorbeeld +31 6 12345678) of een e-mailadres in.",
		"err_empty":      "Schrijf eerst een bericht.",
		"err_too_long":   "Het bericht is te lang.",
		"err_link":       "Links kunnen vanaf dit punt niet worden verstuurd. Beschrijf het in je eigen woorden.",
		"err_control":    "Het bericht bevat een teken dat hier niet kan worden verstuurd.",
		"err_name":       "De naam is te lang of bevat een teken dat hier niet kan worden verstuurd (geen dubbele punt, geen @, geen vierkante haken).",
		"err_quota":      "Vanaf deze telefoon kun je nu even geen berichten meer versturen. Probeer het later opnieuw of vraag het aan de medewerker.",
		"err_other":      "Er ging hier iets mis. Zeg het tegen de medewerker.",
		"language":       "Taal",
		"powered":        "MeshSat Berichtenpunt",
		"warn_title":     "Publieke waarschuwing",
		"warn_test":      "TEST",
		"warn_update":    "Update",
		"warn_until":     "tot",
		// Added for MESHSAT-1803.
		"days_one":                "%d dag",
		"days_other":              "%d dagen",
		"chars_remaining_one":     "nog %d teken, geen links",
		"chars_remaining_other":   "nog %d tekens, geen links",
		"err_too_long_by_one":     "Het bericht is %d teken te lang.",
		"err_too_long_by_other":   "Het bericht is %d tekens te lang.",
		"reply_hub_not_sent":      "Niet verstuurd. Zeg het tegen de medewerker. Reden van de MeshSat Hub (in het Engels): %s",
		"operator_not_set":        "(niet opgegeven)",
		"warn_headline":           "%s (%s)",
		"warn_label":              "Doorgestuurde waarschuwing (%s), uitgegeven %s, via MeshSat. Geen officiële NL-Alert.",
		"warn_src_dwd":            "DWD",
		"warn_src_meteoalarm_nl":  "KNMI via MeteoAlarm",
		"warn_src_test":           "MeshSat-test",
		"warn_src_unknown":        "onbekende bron",
		"warn_event_other":        "Overig",
		"warn_event_wind":         "Wind",
		"warn_event_rain":         "Regen",
		"warn_event_snow_ice":     "Sneeuw/gladheid",
		"warn_event_thunderstorm": "Onweer",
		"warn_event_fog":          "Mist",
		"warn_event_heat":         "Hitte",
		"warn_event_cold":         "Kou",
		"warn_event_coastal":      "Kust",
		"warn_event_flood":        "Overstroming",
		"warn_event_avalanche":    "Lawines",
		"warn_event_wildfire":     "Natuurbrand",
		"warn_event_tornado":      "Tornado",
		"warn_sev_unknown":        "gevaar onbekend",
		"warn_sev_minor":          "gering gevaar",
		"warn_sev_moderate":       "matig gevaar",
		"warn_sev_severe":         "groot gevaar",
		"warn_sev_extreme":        "extreem gevaar",
	},
	"de": {
		"lang_name":      "Deutsch",
		"title":          "Nachrichtenstelle",
		"nav_write":      "Schreiben",
		"nav_pickup":     "Abholen",
		"nav_board":      "Ich bin in Sicherheit",
		"nav_about":      "Über diese Stelle",
		"write_intro":    "Schreiben Sie eine kurze Nachricht an eine Telefonnummer oder eine E-Mail-Adresse. Die Nachricht wird per Satellit verschickt. Die Antwort kommt hierher zurück: Bewahren Sie Ihren Abholcode auf.",
		"recipient":      "An (Telefonnummer mit Ländervorwahl oder E-Mail-Adresse)",
		"recipient_hint": "+49 151 12345678 oder name@beispiel.de",
		"name":           "Ihr Name (freiwillig)",
		"message":        "Nachricht",
		"chars_left":     "höchstens %d Zeichen, keine Links",
		"emergency":      "Das ist ein Notfall",
		"emergency_hint": "Eine Notfallnachricht wird zuerst gesendet und ist beim Empfänger als SOS markiert. Das ist kein Notruf: Die 112 wird nicht benachrichtigt.",
		"send":           "Senden",
		"sent_title":     "Ihr Abholcode",
		"sent_intro":     "Machen Sie ein Bildschirmfoto von diesem Code. Damit holen Sie die Antwort ab. Diese Seite speichert nur zwei Cookies auf Ihrem Telefon: Ihre Sprache und, für einen Tag, welche Nachrichten von Ihnen sind.",
		"sent_pending":   "Das Team dieser Stelle prüft jede Nachricht, bevor sie hinausgeht. Kommen Sie später mit Ihrem Code wieder.",
		"sent_queued":    "Ihre Nachricht ist unterwegs. Per Satellit kann das Minuten bis Stunden dauern. Kommen Sie später mit Ihrem Code wieder.",
		"sent_to":        "An",
		"pickup_title":   "Antwort abholen",
		"pickup_intro":   "Geben Sie den Code ein, den Sie erhalten haben.",
		"code":           "Abholcode",
		"lookup":         "Abrufen",
		"status":         "Status",
		"status_pending": "wartet auf das Team",
		"status_queued":  "wartet auf den Satelliten",
		"status_sent":    "gesendet, noch keine Antwort",
		"status_replied": "Antwort erhalten",
		"status_refused": "nicht gesendet (vom Team abgelehnt)",
		"status_failed":  "nicht gesendet (Übertragung fehlgeschlagen)",
		"replies":        "Antworten",
		"no_replies":     "Noch keine Antwort. Sehen Sie später wieder nach.",
		"unknown_code":   "Dieser Code ist hier nicht bekannt. Vergleichen Sie ihn mit Ihrem Bildschirmfoto.",
		"your_posts":     "Ihre Nachrichten von diesem Telefon",
		"board_title":    "Ich bin in Sicherheit",
		"board_intro":    "Eine Liste, in der man nach Namen suchen kann. Ob Sie Ihren Namen eintragen, entscheiden Sie selbst; der Eintrag bleibt %s stehen und wird nirgendwohin gesendet.",
		"board_name":     "Name",
		"board_status":   "Status (zum Beispiel: in Sicherheit, in der Schule, suche nach)",
		"board_place":    "Ort (freiwillig)",
		"board_add":      "Mich eintragen",
		"board_search":   "Nach Namen suchen",
		"board_find":     "Suchen",
		"board_empty":    "Niemand mit diesem Namen auf der Liste.",
		"board_added":    "Sie stehen jetzt auf der Liste.",
		"about_title":    "Über diese Stelle",
		"about_operator": "Diese Stelle wird betrieben von",
		"about_how":      "Eine Nachricht, die Sie hier schreiben, verlässt diese Stelle über eine Satellitenverbindung zum MeshSat Hub, der sie als SMS oder E-Mail vom Konto des Betreibers verschickt. Die Antwort kommt auf demselben Weg zurück und wartet hier unter Ihrem Abholcode.",
		"about_cost":     "Jede Nachricht und jede Antwort kostet den Betreiber Satellitenguthaben. Fassen Sie sich kurz und senden Sie dieselbe Nachricht nicht zweimal.",
		"data_title":     "Was gespeichert wird",
		"data_notice":    "Gespeichert wird hier: Ihre Nachricht, die Adresse, an die Sie geschrieben haben, der Name, den Sie angegeben haben, die Antwort und ein Zähler pro Telefon, damit ein einzelnes Telefon nicht alles aufbraucht. Gelöscht wird alles %s, nachdem der Betreiber die Stelle schließt, oder früher durch den Betreiber; das Sendeprotokoll des Kits behält den gesendeten Text gemäß seiner eigenen Aufbewahrungsfrist. Weitergesendet werden nur die Adresse, die Nachricht, Ihr Name und der Abholcode. Die Liste „Ich bin in Sicherheit“ ist davon getrennt, enthält nur, was Sie selbst eintragen, und ein Eintrag bleibt %s stehen. Auf Ihrem Telefon: zwei Cookies, Ihre Sprache für 30 Tage und für einen Tag, welche Nachrichten von Ihnen sind. Kein Konto, keine App, kein Tracking. Rechtsgrundlage ist der Schutz lebenswichtiger Interessen von Ihnen oder einer anderen Person; verantwortlich für die Daten ist der oben genannte Betreiber.",
		"done":           "Fertig",
		"done_title":     "Fertig",
		"done_text":      "Sie können diese Seite schließen. Um später eine Antwort zu lesen, verbinden Sie sich wieder mit diesem WLAN und öffnen Sie eine beliebige Webseite.",
		"closed_title":   "Diese Stelle ist geschlossen",
		"closed_text":    "Hier kann keine neue Nachricht mehr geschrieben werden. Eine Antwort können Sie mit Ihrem Code noch bis %s abholen; danach wird alles gelöscht.",
		"err_recipient":  "Geben Sie eine Telefonnummer mit Ländervorwahl (zum Beispiel +49 151 12345678) oder eine E-Mail-Adresse ein.",
		"err_empty":      "Schreiben Sie zuerst eine Nachricht.",
		"err_too_long":   "Die Nachricht ist zu lang.",
		"err_link":       "Links können hier nicht gesendet werden. Beschreiben Sie es lieber mit Worten.",
		"err_control":    "Die Nachricht enthält ein Zeichen, das hier nicht gesendet werden kann.",
		"err_name":       "Der Name ist zu lang oder enthält ein Zeichen, das hier nicht gesendet werden kann (kein Doppelpunkt, kein @, keine eckigen Klammern).",
		"err_quota":      "Von diesem Telefon können gerade keine weiteren Nachrichten gesendet werden. Versuchen Sie es später noch einmal oder fragen Sie das Team.",
		"err_other":      "Hier ist etwas schiefgegangen. Bitte sagen Sie dem Team Bescheid.",
		"language":       "Sprache",
		"powered":        "MeshSat Nachrichtenstelle",
		"warn_title":     "Öffentliche Warnung",
		"warn_test":      "TEST",
		"warn_update":    "Aktualisierung",
		"warn_until":     "bis",
		// Added for MESHSAT-1803.
		"days_one":                "%d Tag",
		"days_other":              "%d Tage",
		"chars_remaining_one":     "noch %d Zeichen, keine Links",
		"chars_remaining_other":   "noch %d Zeichen, keine Links",
		"err_too_long_by_one":     "Die Nachricht ist %d Zeichen zu lang. Bitte kürzen Sie sie.",
		"err_too_long_by_other":   "Die Nachricht ist %d Zeichen zu lang. Bitte kürzen Sie sie.",
		"reply_hub_not_sent":      "Nicht gesendet. Bitte sagen Sie dem Team Bescheid. Grund vom MeshSat Hub (auf Englisch): %s",
		"operator_not_set":        "(nicht angegeben)",
		"warn_headline":           "%s (%s)",
		"warn_label":              "Weitergeleitete Warnung (%s), ausgegeben %s, über MeshSat. Keine offizielle NL-Alert-Meldung.",
		"warn_src_dwd":            "DWD",
		"warn_src_meteoalarm_nl":  "KNMI über MeteoAlarm",
		"warn_src_test":           "MeshSat-Test",
		"warn_src_unknown":        "unbekannte Quelle",
		"warn_event_other":        "Sonstiges",
		"warn_event_wind":         "Wind",
		"warn_event_rain":         "Regen",
		"warn_event_snow_ice":     "Schnee/Eis",
		"warn_event_thunderstorm": "Gewitter",
		"warn_event_fog":          "Nebel",
		"warn_event_heat":         "Hitze",
		"warn_event_cold":         "Kälte",
		"warn_event_coastal":      "Küste",
		"warn_event_flood":        "Hochwasser",
		"warn_event_avalanche":    "Lawinen",
		"warn_event_wildfire":     "Waldbrand",
		"warn_event_tornado":      "Tornado",
		"warn_sev_unknown":        "Gefahr unbekannt",
		"warn_sev_minor":          "geringe Gefahr",
		"warn_sev_moderate":       "mäßige Gefahr",
		"warn_sev_severe":         "große Gefahr",
		"warn_sev_extreme":        "extreme Gefahr",
	},
	"el": {
		"lang_name":      "Ελληνικά",
		"title":          "Σημείο Μηνυμάτων",
		"nav_write":      "Γράψτε",
		"nav_pickup":     "Παραλαβή",
		"nav_board":      "Είμαι καλά",
		"nav_about":      "Σχετικά με το σημείο",
		"write_intro":    "Γράψτε ένα σύντομο μήνυμα προς έναν αριθμό τηλεφώνου ή μια διεύθυνση email. Φεύγει μέσω δορυφόρου. Η απάντηση έρχεται εδώ: κρατήστε τον κωδικό παραλαβής σας.",
		"recipient":      "Προς (αριθμός τηλεφώνου με κωδικό χώρας ή email)",
		"recipient_hint": "+30 691 234 5678 ή onoma@example.gr",
		"name":           "Το όνομά σας (προαιρετικό)",
		"message":        "Μήνυμα",
		"chars_left":     "έως %d χαρακτήρες, χωρίς συνδέσμους",
		"emergency":      "Είναι έκτακτη ανάγκη",
		"emergency_hint": "Ένα μήνυμα έκτακτης ανάγκης φεύγει πρώτο και όποιος το λάβει το βλέπει σημειωμένο ως SOS. Δεν ειδοποιεί το 112.",
		"send":           "Αποστολή",
		"sent_title":     "Ο κωδικός παραλαβής σας",
		"sent_intro":     "Τραβήξτε στιγμιότυπο οθόνης (screenshot) με αυτόν τον κωδικό. Με αυτόν παραλαμβάνετε την απάντηση. Η σελίδα αφήνει στο τηλέφωνό σας μόνο δύο cookies: τη γλώσσα σας και, για μία ημέρα, ποια μηνύματα είναι δικά σας.",
		"sent_pending":   "Ένας εθελοντής σε αυτό το σημείο ελέγχει κάθε μήνυμα πριν φύγει. Ελάτε ξανά αργότερα με τον κωδικό σας.",
		"sent_queued":    "Το μήνυμά σας είναι καθ’ οδόν. Μέσω δορυφόρου μπορεί να πάρει από λεπτά έως ώρες. Ελάτε ξανά αργότερα με τον κωδικό σας.",
		"sent_to":        "Προς",
		"pickup_title":   "Παραλαβή απάντησης",
		"pickup_intro":   "Γράψτε τον κωδικό που σας δόθηκε.",
		"code":           "Κωδικός παραλαβής",
		"lookup":         "Αναζήτηση",
		"status":         "Κατάσταση",
		"status_pending": "περιμένει τον εθελοντή",
		"status_queued":  "περιμένει τον δορυφόρο",
		"status_sent":    "στάλθηκε, χωρίς απάντηση ακόμα",
		"status_replied": "ήρθε απάντηση",
		"status_refused": "δεν στάλθηκε (απορρίφθηκε από τον εθελοντή)",
		"status_failed":  "δεν στάλθηκε (η αποστολή απέτυχε)",
		"replies":        "Απαντήσεις",
		"no_replies":     "Καμία απάντηση ακόμα. Ελέγξτε ξανά αργότερα.",
		"unknown_code":   "Αυτός ο κωδικός δεν υπάρχει σε αυτό το σημείο. Συγκρίνετέ τον με το στιγμιότυπο οθόνης σας.",
		"your_posts":     "Τα μηνύματά σας από αυτό το τηλέφωνο",
		"board_title":    "Είμαι καλά",
		"board_intro":    "Ένας πίνακας όπου μπορεί κανείς να ψάξει κάποιον με το όνομά του. Αν θα γράψετε το όνομά σας, το αποφασίζετε εσείς· μένει %s και δεν στέλνεται πουθενά.",
		"board_name":     "Όνομα",
		"board_status":   "Κατάσταση (π.χ. καλά, στο σχολείο, ψάχνω τον/την)",
		"board_place":    "Τοποθεσία (προαιρετικό)",
		"board_add":      "Προσθέστε με στον πίνακα",
		"board_search":   "Αναζήτηση με όνομα",
		"board_find":     "Εύρεση",
		"board_empty":    "Δεν βρέθηκε κανείς με αυτό το όνομα.",
		"board_added":    "Είστε στον πίνακα.",
		"about_title":    "Σχετικά με το σημείο",
		"about_operator": "Αυτό το σημείο το λειτουργεί",
		"about_how":      "Ένα μήνυμα που γράφετε εδώ φεύγει από αυτό το σημείο μέσω δορυφορικής σύνδεσης προς το MeshSat Hub, που το στέλνει ως SMS ή email από τον λογαριασμό του φορέα. Η απάντηση γυρίζει από τον ίδιο δρόμο και περιμένει εδώ με τον κωδικό παραλαβής σας.",
		"about_cost":     "Κάθε μήνυμα και κάθε απάντηση χρεώνεται στον φορέα (κόστος δορυφόρου). Γράψτε σύντομα και μη στείλετε το ίδιο μήνυμα δύο φορές.",
		"data_title":     "Τι αποθηκεύεται",
		"data_notice":    "Αποθηκεύονται σε αυτό το σημείο: το μήνυμά σας, η διεύθυνση στην οποία γράψατε, το όνομα που δώσατε, η απάντηση, και ένας μετρητής ανά τηλέφωνο ώστε να μην εξαντλεί ένα τηλέφωνο τα πάντα. Διαγράφονται %s αφού ο φορέας κλείσει το σημείο, ή νωρίτερα από τον φορέα· το αρχείο αποστολών της συσκευής κρατά το σταλμένο κείμενο σύμφωνα με τη δική του προθεσμία. Προωθούνται μόνο: η διεύθυνση, το μήνυμα, το όνομά σας και ο κωδικός παραλαβής. Ο πίνακας «Είμαι καλά» είναι ξεχωριστός, περιέχει μόνο ό,τι βάζετε εσείς, και κάθε γραμμή μένει %s. Στο τηλέφωνό σας: δύο cookies, η γλώσσα σας για 30 ημέρες και για μία ημέρα ποια μηνύματα είναι δικά σας. Χωρίς λογαριασμό, χωρίς εφαρμογή, χωρίς παρακολούθηση. Η νομική βάση είναι η προστασία ζωτικών συμφερόντων, δικών σας ή άλλου προσώπου· υπεύθυνος για τα δεδομένα είναι ο φορέας που αναφέρεται παραπάνω.",
		"done":           "Τέλος",
		"done_title":     "Έτοιμο",
		"done_text":      "Μπορείτε να κλείσετε αυτή τη σελίδα. Για να διαβάσετε αργότερα μια απάντηση, συνδεθείτε ξανά σε αυτό το WiFi και ανοίξτε οποιαδήποτε ιστοσελίδα.",
		"closed_title":   "Αυτό το σημείο έκλεισε",
		"closed_text":    "Δεν μπορείτε πια να γράψετε νέο μήνυμα εδώ. Μπορείτε ακόμα να παραλάβετε μια απάντηση με τον κωδικό σας μέχρι %s· μετά διαγράφονται όλα.",
		"err_recipient":  "Γράψτε έναν αριθμό τηλεφώνου με κωδικό χώρας (π.χ. +30 691 234 5678) ή μια διεύθυνση email.",
		"err_empty":      "Γράψτε πρώτα ένα μήνυμα.",
		"err_too_long":   "Το μήνυμα είναι πολύ μεγάλο. Γράψτε το πιο σύντομα.",
		"err_link":       "Από αυτό το σημείο δεν στέλνονται σύνδεσμοι. Περιγράψτε το με λόγια.",
		"err_control":    "Το μήνυμα περιέχει χαρακτήρα που αυτό το σημείο δεν μπορεί να στείλει.",
		"err_name":       "Το όνομα είναι πολύ μεγάλο ή περιέχει χαρακτήρα που δεν στέλνεται (όχι άνω-κάτω τελεία, όχι @, όχι αγκύλες).",
		"err_quota":      "Από αυτό το τηλέφωνο δεν μπορούν να σταλούν άλλα μηνύματα προς το παρόν. Δοκιμάστε αργότερα ή ρωτήστε τον εθελοντή.",
		"err_other":      "Κάτι πήγε στραβά. Ενημερώστε τον εθελοντή.",
		"language":       "Γλώσσα",
		"powered":        "MeshSat Σημείο Μηνυμάτων",
		"warn_title":     "Δημόσια προειδοποίηση",
		"warn_test":      "ΔΟΚΙΜΗ",
		"warn_update":    "Ενημέρωση",
		"warn_until":     "έως",
		// Added for MESHSAT-1803.
		"days_one":                "%d ημέρα",
		"days_other":              "%d ημέρες",
		"chars_remaining_one":     "απομένει %d χαρακτήρας, χωρίς συνδέσμους",
		"chars_remaining_other":   "απομένουν %d χαρακτήρες, χωρίς συνδέσμους",
		"err_too_long_by_one":     "Το μήνυμα είναι μεγαλύτερο κατά %d χαρακτήρα. Γράψτε το πιο σύντομα.",
		"err_too_long_by_other":   "Το μήνυμα είναι μεγαλύτερο κατά %d χαρακτήρες. Γράψτε το πιο σύντομα.",
		"reply_hub_not_sent":      "Δεν στάλθηκε. Ενημερώστε τον εθελοντή. Αιτία από το MeshSat Hub (στα αγγλικά): %s",
		"operator_not_set":        "(δεν έχει δηλωθεί)",
		"warn_headline":           "%s (%s)",
		"warn_label":              "Αναμετάδοση προειδοποίησης (%s), εκδόθηκε %s, μέσω MeshSat. Δεν είναι επίσημο μήνυμα NL-Alert.",
		"warn_src_dwd":            "DWD",
		"warn_src_meteoalarm_nl":  "KNMI μέσω MeteoAlarm",
		"warn_src_test":           "δοκιμή MeshSat",
		"warn_src_unknown":        "άγνωστη πηγή",
		"warn_event_other":        "Άλλο",
		"warn_event_wind":         "Άνεμος",
		"warn_event_rain":         "Βροχή",
		"warn_event_snow_ice":     "Χιόνι/πάγος",
		"warn_event_thunderstorm": "Καταιγίδα",
		"warn_event_fog":          "Ομίχλη",
		"warn_event_heat":         "Καύσωνας",
		"warn_event_cold":         "Κρύο",
		"warn_event_coastal":      "Ακτές",
		"warn_event_flood":        "Πλημμύρα",
		"warn_event_avalanche":    "Χιονοστιβάδα",
		"warn_event_wildfire":     "Δασική πυρκαγιά",
		"warn_event_tornado":      "Ανεμοστρόβιλος",
		"warn_sev_unknown":        "άγνωστος κίνδυνος",
		"warn_sev_minor":          "μικρός κίνδυνος",
		"warn_sev_moderate":       "μέτριος κίνδυνος",
		"warn_sev_severe":         "σοβαρός κίνδυνος",
		"warn_sev_extreme":        "ακραίος κίνδυνος",
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

// Plural is a count in words: key_one for exactly one, key_other for every
// other whole number (the rule English, German, Dutch and Greek share),
// formatted with n.
func Plural(lang, key string, n int) string {
	form := key + "_other"
	if n == 1 {
		form = key + "_one"
	}
	return fmt.Sprintf(T(lang, form), n)
}

// dateLayouts are the dates as each language writes them: 6 Dec 2026,
// 6.12.2026, 6-12-2026, 6/12/2026. The English month is the only name.
var dateLayouts = map[string]string{
	"en": "2 Jan 2006",
	"de": "2.1.2006",
	"nl": "2-1-2006",
	"el": "2/1/2006",
}

// FormatDate is t's day as lang writes it, in t's own zone (the caller
// puts t in the kit's).
func FormatDate(lang string, t time.Time) string {
	layout, ok := dateLayouts[lang]
	if !ok {
		layout = dateLayouts[DefaultLanguage]
	}
	return t.Format(layout)
}

// FormatClock is t's time of day, 24-hour, as all four languages write it.
func FormatClock(t time.Time) string { return t.Format("15:04") }

// FormatDateTime is the day and the time of day.
func FormatDateTime(lang string, t time.Time) string {
	return FormatDate(lang, t) + " " + FormatClock(t)
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
