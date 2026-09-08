package main

import "regexp"

// rule is a high-precision lexical marker for one category. Rules run before
// the statistical classifier: when a request literally says "traduis" or
// carries a fenced code block, no model is needed to name its category, and a
// small Naive Bayes trained on a few hundred examples is more likely to get
// it wrong than right.
type rule struct {
	category string
	pattern  *regexp.Regexp
}

// rules are ordered by precedence: the first match wins. Code comes first
// because a request may ask to translate or summarize a snippet, and the
// snippet is what decides which model can help.
var rules = []rule{
	{"code", regexp.MustCompile("(?s)```|~~~")},
	{"code", regexp.MustCompile(`(?m)^\s*(func|def|class|import|from|package|public|private|static|const|let|var|SELECT|INSERT|UPDATE|CREATE TABLE)\b`)},
	{"code", regexp.MustCompile(`(?i)\b(stack ?trace|traceback|segfault|nil pointer|null pointer|undefined is not a function|ne compile pas|does not compile|doesn't compile|compile error|unit tests?|tests? unitaires?|refactor|pull request|merge request|regex|dockerfile|kubernetes|sql|api rest|endpoint|middleware|goroutine|javascript|typescript|python|golang|rust|kotlin|java|c\+\+|php|ruby|bash|npm|pip|cargo)\b`)},
	{"translation", regexp.MustCompile(`(?i)\b(tradui[st]|traduire|traduction|translate|translation|comment dit-on|how do you say|in (english|french|spanish|german|italian|portuguese|dutch|japanese|chinese|arabic|russian)\b.*\?|en (anglais|français|espagnol|allemand|italien|portugais|néerlandais|japonais|chinois|arabe|russe)\b)`)},
	{"summarization", regexp.MustCompile(`(?i)\b(résume[rz]?|résumé|synthétise[rz]?|synthèse|summari[sz]e|summary|tl;?dr|condense|key takeaways|executive summary|gist|boil down|abstract of)\b`)},
	{"rewriting", regexp.MustCompile(`(?i)\b(reformule[rz]?|réécri[st]|réécrire|rewrite|rephrase|paraphrase|corrige[rz]? (l'orthographe|la grammaire|les fautes|la ponctuation)|proofread|orthographe|grammaire|fautes de frappe|typos?|rends ce (texte|mail|paragraphe)|make (this|it) (more|less|sound)|simplifie|raccourcis|shorten|polish this|active voice|passive voice|tutoiement|vouvoiement)\b`)},
	{"math", regexp.MustCompile(`(?i)\b(résous|résoudre|solve|équation|equation|dérivée|derivative|intégrale|integral|calcule[rz]?|compute|probabilité|probability|théorème|theorem|démontre|prove that|factorise|factor|simplif(y|ie)|matrice|matrix|eigenvalue|pgcd|gcd|modulo|racine carrée|square root|logarithme|logarithm|combien font|what is \d|% de \d|choose \d)\b|[0-9]\s*[+\-×x*/^=]\s*[0-9x]|x²|x\^2|√`)},
	{"creative", regexp.MustCompile(`(?i)\b(poème|poem|haïku|haiku|sonnet|limerick|nouvelle|short story|conte|fairy tale|fable|légende|legend|mythe|myth|chanson|song lyrics|lyrics|rap|slogan|comptine|lullaby|monologue|scène|scene where|plot twist|imagine (un|une|a|the)|invente|make up a|bedtime story|micro-?fiction|roman|novel)\b`)},
	{"instruction", regexp.MustCompile(`(?i)\b(comment (faire|configurer|installer|créer|préparer|obtenir|déclarer|résilier|nettoyer|fabriquer|changer|enregistrer|organiser|démarrer)|how (do|can|should) i\b|how to\b|step[- ]by[- ]step|étape par étape|marche à suivre|procédure|guide me|walk me through|show me how|instructions (to|for)|quelles (sont les )?étapes|quelle est la marche)\b`)},
}

// shortChatMaxWords is the length under which a request with no other marker
// is treated as small talk: greetings, thanks, acknowledgements.
const shortChatMaxWords = 6

var reWord = regexp.MustCompile(`[\p{L}\p{N}']+`)

// applyRules returns the category named by the first matching rule, or "".
func applyRules(text string) string {
	for _, r := range rules {
		if r.pattern.MatchString(text) {
			return r.category
		}
	}
	return ""
}

func wordCount(text string) int {
	return len(reWord.FindAllString(text, -1))
}
