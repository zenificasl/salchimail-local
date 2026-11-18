package main

type translator func(key string) string

func translate(lang string) translator {
	return func(key string) string {
		if s, ok := messages[lang][key]; ok {
			return s
		}
		if s, ok := messages["en"][key]; ok {
			return s
		}
		return key
	}
}

var messages = map[string]map[string]string{
	"en": {
		"own_reply":       "Replied to you on %s",
		"own_click":       "Clicked a link in your email on %s (clicks can be automatic)",
		"own_open":        "Opened your email on %s (opens can be automatic)",
		"own_delivered":   "Accepted without a bounce on %s (does not prove it reached the inbox)",
		"own_hard_bounce": "Your own email hard-bounced on %s",

		"syntax_invalid":      "Not a valid email address",
		"address_unsupported": "Internationalised (non-ASCII) address: it cannot be checked",
		"disposable_domain":   "Disposable email domain",
		"legacy_domain":       "Domain of a discontinued email provider",
		"domain_null_mx":      "The domain declares that it accepts no mail (null MX)",
		"domain_no_mail_host": "The domain has no MX and no address record: it cannot receive mail",

		"mailbox_not_checked":    "The domain can receive mail; the mailbox was not checked (run with --budget to check it)",
		"would_send":             "Dry run: would be sent to the API for a mailbox check",
		"would_send_conditional": "Dry run: would be sent only if the first address of its domain shows that the server tells mailboxes apart",
		"budget_exhausted":       "Not sent: the credit budget was used up",
		"batch_pending":          "The batch has not finished; run the same command again",
		"inferred_catch_all":     "Not sent: the server accepts any recipient (seen with another address of this domain), so checking this mailbox would add nothing",
		"inferred_blocked":       "Not sent: the server did not let another address of this domain be checked (%s), so this one would not be resolved either",
		"inferred_domain":        "Not sent: the API answered %s for another address of this domain",

		"sum_title":       "salchimail-local clean: %d rows, %d unique addresses",
		"sum_local":       "Resolved on this machine: %d",
		"sum_own":         "  with your own history: %d",
		"sum_rules":       "  with syntax and domain lists: %d",
		"sum_dns":         "  with DNS: %d",
		"sum_sent":        "Sent to the SalchiMail API: %d (mailbox credits charged: %d)",
		"sum_inferred":    "Resolved without being sent, from the answer for another address of the domain: %d",
		"sum_private":     "Never left this machine: %d of %d addresses, and every row of your history.",
		"sum_not_checked": "Not checked: %d",
		"sum_actions":     "Decisions: send %d, do_not_send %d, review %d, not_checked %d",
		"sum_test":        "Test-mode key: the API results are simulated.",
		"sum_hint_budget": "%d addresses can only be resolved by a mailbox check. Preview it with --dry-run, then run with --budget N.",

		"dry_title":     "DRY RUN: nothing was sent. A real run would send these addresses, and only these, to POST %s/batches in mailbox mode:",
		"dry_wave1":     "  Wave 1, one address per domain (%d):",
		"dry_wave2":     "  Wave 2, each one only if wave 1 shows that its server tells mailboxes apart (up to %d):",
		"dry_budget":    "  Budget: at most %d mailbox credits; addresses beyond it are not sent.",
		"dry_no_budget": "  No --budget given: a real run needs one and stops sending when it is used up.",
		"dry_private":   "  Your history and every other address stay on this machine.",
		"pending":       "A batch is still running. Run the same command again to collect its results (state kept in %s).",

		"reh_title":   "Sign-up policy rehearsal: %d sign-ups (%d good: paid or activated; %d junk: flagged or hard-bounced; %d with no outcome)",
		"reh_header":  "RULE\tWOULD BLOCK\tGOOD LOST\tJUNK STOPPED\tNO OUTCOME\tWHAT IT DOES",
		"reh_checked": "Mailbox rules use the %d of %d addresses that got an answer from the API.",
		"reh_caveats": "Read with care: addresses are checked as they are today, not as they were on the sign-up date; sign-ups that an earlier rule already rejected are not in your data; and small counts prove little.",

		"rule_reject_syntax":                   "Reject addresses with invalid syntax",
		"rule_require_mx":                      "Reject domains that cannot receive mail",
		"rule_reject_invalid":                  "Reject what basic mode calls invalid (the two rules above)",
		"rule_block_disposable":                "Block disposable domains",
		"rule_block_legacy":                    "Block domains of discontinued providers",
		"rule_block_free":                      "Block free webmail (Gmail, Outlook, Yahoo...)",
		"rule_reject_invalid_block_disposable": "Reject invalid and block disposable (the documented sign-up pattern)",
		"rule_mailbox_reject_invalid":          "Reject what mailbox mode calls invalid",
		"rule_mailbox_reject_invalid_risky":    "Reject invalid and risky in mailbox mode",
	},
	"es": {
		"own_reply":       "Te escribió el %s",
		"own_click":       "Hizo clic en un enlace de tu correo el %s (los clics pueden ser automáticos)",
		"own_open":        "Abrió tu correo el %s (las aperturas pueden ser automáticas)",
		"own_delivered":   "Aceptado sin rebote el %s (no prueba que llegara a la bandeja)",
		"own_hard_bounce": "Tu propio correo rebotó (rebote duro) el %s",

		"syntax_invalid":      "No es una dirección de email válida",
		"address_unsupported": "Dirección internacional (no ASCII): no se puede comprobar",
		"disposable_domain":   "Dominio de correo desechable",
		"legacy_domain":       "Dominio de un proveedor de correo desaparecido",
		"domain_null_mx":      "El dominio declara que no acepta correo (MX nulo)",
		"domain_no_mail_host": "El dominio no tiene MX ni registro de dirección: no puede recibir correo",

		"mailbox_not_checked":    "El dominio puede recibir correo; el buzón no se ha comprobado (ejecuta con --budget para comprobarlo)",
		"would_send":             "Ensayo: se enviaría a la API para comprobar el buzón",
		"would_send_conditional": "Ensayo: se enviaría solo si la primera dirección de su dominio muestra que el servidor distingue buzones",
		"budget_exhausted":       "No enviada: se agotó el presupuesto de créditos",
		"batch_pending":          "El lote no ha terminado; ejecuta de nuevo la misma orden",
		"inferred_catch_all":     "No enviada: el servidor acepta cualquier destinatario (visto con otra dirección de este dominio), así que comprobar este buzón no aportaría nada",
		"inferred_blocked":       "No enviada: el servidor no dejó comprobar otra dirección de este dominio (%s), así que esta tampoco se resolvería",
		"inferred_domain":        "No enviada: la API respondió %s para otra dirección de este dominio",

		"sum_title":       "salchimail-local clean: %d filas, %d direcciones únicas",
		"sum_local":       "Resueltas en esta máquina: %d",
		"sum_own":         "  con tu propio historial: %d",
		"sum_rules":       "  con sintaxis y listas de dominios: %d",
		"sum_dns":         "  con DNS: %d",
		"sum_sent":        "Enviadas a la API de SalchiMail: %d (créditos de buzón cobrados: %d)",
		"sum_inferred":    "Resueltas sin enviarlas, con la respuesta a otra dirección del dominio: %d",
		"sum_private":     "Nunca salieron de esta máquina: %d de %d direcciones, y todas las filas de tu historial.",
		"sum_not_checked": "Sin comprobar: %d",
		"sum_actions":     "Decisiones: send %d, do_not_send %d, review %d, not_checked %d",
		"sum_test":        "Clave de pruebas: los resultados de la API son simulados.",
		"sum_hint_budget": "%d direcciones solo se pueden resolver comprobando el buzón. Míralo antes con --dry-run y luego ejecuta con --budget N.",

		"dry_title":     "ENSAYO (--dry-run): no se ha enviado nada. Una ejecución real enviaría estas direcciones, y solo estas, a POST %s/batches en modo mailbox:",
		"dry_wave1":     "  Ola 1, una dirección por dominio (%d):",
		"dry_wave2":     "  Ola 2, cada una solo si la ola 1 muestra que su servidor distingue buzones (hasta %d):",
		"dry_budget":    "  Presupuesto: como máximo %d créditos de buzón; las direcciones que lo superen no se envían.",
		"dry_no_budget": "  Sin --budget: la ejecución real necesita uno y deja de enviar cuando se agota.",
		"dry_private":   "  Tu historial y todas las demás direcciones se quedan en esta máquina.",
		"pending":       "Hay un lote en curso. Ejecuta de nuevo la misma orden para recoger sus resultados (estado guardado en %s).",

		"reh_title":   "Ensayo de política de registro: %d altas (%d buenas: pagaron o se activaron; %d basura: marcadas o con rebote duro; %d sin desenlace)",
		"reh_header":  "REGLA\tBLOQUEARÍA\tBUENAS PERDIDAS\tBASURA FRENADA\tSIN DESENLACE\tQUÉ HACE",
		"reh_checked": "Las reglas de buzón usan las %d de %d direcciones que obtuvieron respuesta de la API.",
		"reh_caveats": "Léelo con cuidado: las direcciones se comprueban como están hoy, no como estaban el día del alta; las altas que una regla anterior ya rechazó no están en tus datos; y los recuentos pequeños prueban poco.",

		"rule_reject_syntax":                   "Rechazar direcciones con sintaxis inválida",
		"rule_require_mx":                      "Rechazar dominios que no pueden recibir correo",
		"rule_reject_invalid":                  "Rechazar lo que el modo basic llama invalid (las dos reglas anteriores)",
		"rule_block_disposable":                "Bloquear dominios desechables",
		"rule_block_legacy":                    "Bloquear dominios de proveedores desaparecidos",
		"rule_block_free":                      "Bloquear correo gratuito (Gmail, Outlook, Yahoo...)",
		"rule_reject_invalid_block_disposable": "Rechazar invalid y bloquear desechables (el patrón documentado para registros)",
		"rule_mailbox_reject_invalid":          "Rechazar lo que el modo mailbox llama invalid",
		"rule_mailbox_reject_invalid_risky":    "Rechazar invalid y risky en modo mailbox",
	},
}
