package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/modelstore"
	"github.com/bornholm/go-anon/pkg/ner"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// Plugin implémente proto.XoloPluginServer.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	mu         sync.Mutex
	store      *modelstore.Store
	anons      map[string]*anonymizer.Anonymizer
	detector   goanon.LanguageDetector
	candidates []string
	lastCfgKey string

	hostMu     sync.Mutex
	hostClient pluginsdk.HostClient
}

// SetHostClient implémente pluginsdk.HostClientSetter : le runtime l'appelle une
// fois la connexion au XoloHostService établie (dans Initialize).
func (p *Plugin) SetHostClient(c pluginsdk.HostClient) {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	p.hostClient = c
}

func (p *Plugin) getHostClient() pluginsdk.HostClient {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	return p.hostClient
}

// emitEvent émet un événement de façon non bloquante (l'appel gRPC vers l'hôte
// ne doit pas ralentir le traitement de la requête).
func (p *Plugin) emitEvent(evt pluginsdk.Event) {
	hc := p.getHostClient()
	if hc == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hc.EmitEvent(ctx, evt); err != nil {
			slog.Warn("pseudonymizer: could not emit event", slog.Any("error", err))
		}
	}()
}

// countEntities accumule dans counts le nombre d'entités détectées par type.
func countEntities(counts map[string]int, entities []ner.Entity) {
	for _, e := range entities {
		counts[string(e.Type)]++
	}
}

// summarizeEntities met en forme le décompte par type ("LOC:1,PER:2"), trié
// pour rester stable d'un événement à l'autre.
func summarizeEntities(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}

// Motifs d'un passage en passe-plat, publiés dans l'événement associé.
const (
	passthroughConfigError     = "configuration invalide"
	passthroughRequestParse    = "corps de requête illisible"
	passthroughMessagesParse   = "messages illisibles"
	passthroughAnonymizerInit  = "anonymiseur indisponible"
	passthroughMarshalMessages = "sérialisation des messages impossible"
)

// passthroughOnError laisse la requête passer sans pseudonymisation et le
// signale par un événement : un passe-plat silencieux ne laisserait aucune
// trace du fait que des données personnelles ont pu atteindre le modèle. Le
// message d'erreur est publié tel quel, il ne contient jamais de contenu
// utilisateur.
func (p *Plugin) passthroughOnError(ctx context.Context, in *proto.PreRequestInput, reason string, err error) *proto.PreRequestOutput {
	slog.WarnContext(ctx, "pseudonymizer: "+reason+", passing through", slog.Any("error", err))
	attrs := map[string]string{"reason": reason}
	if err != nil {
		attrs["error"] = err.Error()
	}
	p.emitEvent(pluginsdk.Event{
		PluginName: "pseudonymizer",
		OrgID:      in.GetCtx().GetOrgId(),
		UserID:     in.GetCtx().GetUserId(),
		Type:       "passthrough",
		Severity:   "error",
		Message:    "Requête transmise sans pseudonymisation : " + reason,
		Attributes: attrs,
	})
	return passthroughOutput()
}

func newPlugin() *Plugin {
	return &Plugin{
		anons: make(map[string]*anonymizer.Anonymizer),
	}
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:        "pseudonymizer",
		Version:     "0.1.0",
		Description: "Anonymise les messages avant le LLM et rétablit les données originales dans la réponse.",
		Capabilities: []proto.PluginDescriptor_Capability{
			proto.PluginDescriptor_PRE_REQUEST,
			proto.PluginDescriptor_POST_RESPONSE,
		},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request"},
		},
		ConfigSchema:    configSchemaJSON,
		DefaultRequired: true,
	}, nil
}

// passthroughOutput lets the request through untouched. No state is stashed, so
// PostResponse has nothing to restore: the host is told it can stream the
// response live rather than buffer it.
func passthroughOutput() *proto.PreRequestOutput {
	return &proto.PreRequestOutput{Allowed: true, NoResponseRewrite: true}
}

func (p *Plugin) PreRequest(ctx context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg, err := parseConfig(in.GetCtx().GetConfigJson())
	if err != nil {
		return p.passthroughOnError(ctx, in, passthroughConfigError, err), nil
	}

	// Resolve request body: prefer from input port, fall back to Model (full request body JSON).
	requestJSON := ""
	if in.InputsJson != "" {
		var inputs map[string]any
		if err := json.Unmarshal([]byte(in.InputsJson), &inputs); err == nil {
			if v, ok := inputs["request"].(string); ok {
				requestJSON = v
			}
		}
	}
	if requestJSON == "" {
		requestJSON = in.Model
	}

	// Parse full request body to modify the messages field and rebuild it.
	var requestBody map[string]any
	if requestJSON != "" {
		if err := json.Unmarshal([]byte(requestJSON), &requestBody); err != nil {
			return p.passthroughOnError(ctx, in, passthroughRequestParse, err), nil
		}
	}

	// Resolve messages JSON.
	messagesJSON := in.GetMessagesJson()
	if messagesJSON == "" && requestBody != nil {
		if msgs, ok := requestBody["messages"]; ok {
			if b, err := json.Marshal(msgs); err == nil {
				messagesJSON = string(b)
			}
		}
	}
	if messagesJSON == "" {
		return passthroughOutput(), nil
	}

	var messages []map[string]any
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		return p.passthroughOnError(ctx, in, passthroughMessagesParse, err), nil
	}

	// Resolve the language of the conversation (explicit config or automatic
	// detection on the raw, not-yet-anonymized text) and get the matching
	// anonymizer.
	language, anon, err := p.resolveAnonymizer(ctx, cfg, messages)
	if err != nil {
		return p.passthroughOnError(ctx, in, passthroughAnonymizerInit, err), nil
	}

	slog.DebugContext(ctx, "pseudonymizer: anonymizing messages",
		slog.Int("count", len(messages)),
		slog.String("language", language),
		slog.Bool("language_auto", cfg.Language == LanguageAuto),
		slog.String("strategy", cfg.Strategy),
	)

	// Anonymize all text content using a shared session for consistent numbering.
	// Non-anonymizable attachments (documents, files…) are removed and tracked.
	session := anonymizer.NewSession()
	anonymOpts, err := buildAnonymizeOptions(ctx, cfg, in.GetCtx(), p.getHostClient())
	if err != nil {
		// Fail-closed: a hash strategy without a usable key cannot protect
		// anything, so the request is refused rather than forwarded in clear.
		slog.WarnContext(ctx, "pseudonymizer: request blocked, anonymization options unavailable", slog.Any("error", err))
		p.emitEvent(pluginsdk.Event{
			PluginName: "pseudonymizer",
			OrgID:      in.GetCtx().GetOrgId(),
			UserID:     in.GetCtx().GetUserId(),
			Type:       "request.blocked",
			Severity:   "error",
			Message:    "Requête refusée : " + err.Error(),
			Attributes: map[string]string{
				"reason":   "hash_key_missing",
				"error":    err.Error(),
				"strategy": cfg.Strategy,
			},
		})
		return &proto.PreRequestOutput{
			Allowed:         false,
			RejectionReason: "Requête refusée par le pseudonymiseur : " + err.Error() + ". Contactez l'administrateur de l'organisation.",
		}, nil
	}
	var (
		removedParts         []removedPart
		processedAttachments int
		// typeCounts compte les entités détectées par type, pour l'événement.
		typeCounts = map[string]int{}
		// anonymizeFailures compte les contenus transmis tels quels parce que
		// leur anonymisation a échoué : ils ont pu porter des données
		// personnelles jusqu'au modèle.
		anonymizeFailures int
		lastAnonymizeErr  error
	)

	filtered := make([]map[string]any, 0, len(messages))
	for i, msg := range messages {
		role, _ := msg["role"].(string)
		content, ok := msg["content"]
		if !ok {
			filtered = append(filtered, messages[i])
			continue
		}
		switch c := content.(type) {
		case string:
			result, err := anon.Anonymize(c, append(anonymOpts, anonymizer.WithSession(session))...)
			if err != nil {
				if out := handleVerificationError(in, err, cfg, p.getHostClient()); out != nil {
					return out, nil
				}
				slog.WarnContext(ctx, "pseudonymizer: failed to anonymize string content", slog.Any("error", err))
				anonymizeFailures++
				lastAnonymizeErr = err
			} else {
				countEntities(typeCounts, result.Entities)
				messages[i]["content"] = result.Text
			}
			filtered = append(filtered, messages[i])
		case []any:
			var kept []any
			for _, part := range c {
				partMap, ok := part.(map[string]any)
				if !ok {
					kept = append(kept, part)
					continue
				}
				partType, _ := partMap["type"].(string)
				switch {
				case partType == "text":
					text, _ := partMap["text"].(string)
					result, err := anon.Anonymize(text, append(anonymOpts, anonymizer.WithSession(session))...)
					if err != nil {
						if out := handleVerificationError(in, err, cfg, p.getHostClient()); out != nil {
							return out, nil
						}
						slog.WarnContext(ctx, "pseudonymizer: failed to anonymize text part", slog.Any("error", err))
						anonymizeFailures++
						lastAnonymizeErr = err
						kept = append(kept, part)
					} else {
						countEntities(typeCounts, result.Entities)
						updated := make(map[string]any, len(partMap))
						for k, v := range partMap {
							updated[k] = v
						}
						updated["text"] = result.Text
						kept = append(kept, updated)
					}
				case isToolPart(partType):
					// An agent tool block is text the model asked for, not a
					// document someone attached. It carries no inline bytes
					// either, so the attachment path below would declare it
					// unreadable and drop it — leaving an agent whose read
					// tools return nothing, working blind.
					updated, handled, err := anonymizeToolPart(partMap, func(s string) (string, error) {
						result, anonErr := anon.Anonymize(s, append(anonymOpts, anonymizer.WithSession(session))...)
						if anonErr != nil {
							return "", anonErr
						}
						countEntities(typeCounts, result.Entities)
						return result.Text, nil
					})
					if err != nil {
						if out := handleVerificationError(in, err, cfg, p.getHostClient()); out != nil {
							return out, nil
						}
						slog.WarnContext(ctx, "pseudonymizer: failed to anonymize tool part",
							slog.String("type", partType),
							slog.Any("error", err),
						)
						anonymizeFailures++
						lastAnonymizeErr = err
						kept = append(kept, part)
						continue
					}
					if !handled {
						// A non-textual payload inside a tool block — an image
						// returned by a screenshot tool, say. The plugin cannot
						// vouch for it, so it keeps the attachment policy.
						removedParts = append(removedParts, removedPart{
							Role:   role,
							Type:   partType,
							Name:   partName(partMap),
							Reason: reasonNonTextToolPart,
						})
						continue
					}
					kept = append(kept, updated)

				default:
					// Attachment: read it as text when the format allows,
					// anonymize that text and send it in place of the file.
					// The bytes themselves never reach the LLM.
					att := extractAttachment(partMap, cfg.MaxAttachmentBytes)
					text, truncated, err := attachmentText(cfg, att)
					if err != nil {
						removedParts = append(removedParts, removedPart{
							Role:   role,
							Type:   partType,
							Name:   partName(partMap),
							Reason: attachmentReason(err),
						})
						slog.DebugContext(ctx, "pseudonymizer: attachment cannot be pseudonymized",
							slog.String("role", role),
							slog.String("type", partType),
							slog.String("name", att.Name),
							slog.Any("error", err),
						)
						continue
					}

					result, err := anon.Anonymize(text, append(anonymOpts, anonymizer.WithSession(session))...)
					if err != nil {
						if out := handleVerificationError(in, err, cfg, p.getHostClient()); out != nil {
							return out, nil
						}
						// An attachment whose text could not be anonymized must
						// not be forwarded in any shape.
						slog.WarnContext(ctx, "pseudonymizer: failed to anonymize attachment text",
							slog.String("name", att.Name),
							slog.Any("error", err),
						)
						removedParts = append(removedParts, removedPart{
							Role:   role,
							Type:   partType,
							Name:   partName(partMap),
							Reason: reasonAnonymizeFailed,
						})
						continue
					}

					countEntities(typeCounts, result.Entities)
					kept = append(kept, map[string]any{
						"type": "text",
						"text": attachmentTextPart(att, partType, result.Text, truncated),
					})
					processedAttachments++
					slog.DebugContext(ctx, "pseudonymizer: attachment pseudonymized",
						slog.String("role", role),
						slog.String("type", partType),
						slog.String("name", att.Name),
						slog.Bool("truncated", truncated),
					)
				}
			}
			// Drop the message entirely if all its parts were removed.
			if len(kept) > 0 {
				messages[i]["content"] = kept
				filtered = append(filtered, messages[i])
			}
		default:
			filtered = append(filtered, messages[i])
		}
	}

	// An attachment the plugin cannot vouch for is a file the user believes was
	// analyzed. Under the "block" policy the request is refused rather than
	// answered from a silently amputated prompt.
	if len(removedParts) > 0 && cfg.UnsupportedAttachments == "block" {
		p.emitBlockedAttachmentsEvent(in, removedParts)
		slog.InfoContext(ctx, "pseudonymizer: request blocked, attachments cannot be pseudonymized",
			slog.Int("attachments", len(removedParts)),
		)
		return &proto.PreRequestOutput{
			Allowed:         false,
			RejectionReason: blockedAttachmentsReason(removedParts),
		}, nil
	}

	// Inject an instruction asking the LLM to keep placeholder tokens verbatim,
	// so they can be deanonymized in the response.
	filtered = injectPlaceholderInstruction(filtered, session.Mapping, cfg, language)

	// Serialize anonymized+filtered messages.
	modifiedMessagesJSON, err := json.Marshal(filtered)
	if err != nil {
		return p.passthroughOnError(ctx, in, passthroughMarshalMessages, err), nil
	}

	// Rebuild the full request body with filtered messages for the output port.
	var outputsJSON string
	if requestBody != nil {
		requestBody["messages"] = filtered
		if b, err := json.Marshal(requestBody); err == nil {
			outputs := map[string]any{"request": string(b)}
			if ob, err := json.Marshal(outputs); err == nil {
				outputsJSON = string(ob)
			}
		}
	}

	// Serialize full state (mapping + removed parts) for PostResponse.
	state := pluginState{
		Mapping:      session.Mapping,
		RemovedParts: removedParts,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer: failed to marshal state", slog.Any("error", err))
		stateJSON = []byte("{}")
	}

	slog.DebugContext(ctx, "pseudonymizer: anonymization done",
		slog.Int("entities", len(session.Mapping)),
		slog.Int("processed_attachments", processedAttachments),
		slog.Int("removed_attachments", len(removedParts)),
	)

	// Emit an event whenever sensitive data was detected (and pseudonymized) or
	// a non-anonymizable attachment had to be removed.
	if len(session.Mapping) > 0 || len(removedParts) > 0 {
		p.emitEvent(pluginsdk.Event{
			PluginName: "pseudonymizer",
			OrgID:      in.GetCtx().GetOrgId(),
			UserID:     in.GetCtx().GetUserId(),
			Type:       "sensitive-data.detected",
			Severity:   "warning",
			Message:    fmt.Sprintf("Données sensibles détectées et pseudonymisées (%d entité(s))", len(session.Mapping)),
			Attributes: map[string]string{
				"entities":              strconv.Itoa(len(session.Mapping)),
				"types":                 summarizeEntities(typeCounts),
				"processed_attachments": strconv.Itoa(processedAttachments),
				"removed_attachments":   strconv.Itoa(len(removedParts)),
				"language":              language,
			},
		})
	}

	// A content whose anonymization failed was forwarded as is: say so, since
	// nothing in the request itself tells the administrator the filter gave up.
	if anonymizeFailures > 0 {
		p.emitEvent(pluginsdk.Event{
			PluginName: "pseudonymizer",
			OrgID:      in.GetCtx().GetOrgId(),
			UserID:     in.GetCtx().GetUserId(),
			Type:       "passthrough",
			Severity:   "error",
			Message:    fmt.Sprintf("%d contenu(s) transmis sans pseudonymisation : échec de l'anonymisation", anonymizeFailures),
			Attributes: map[string]string{
				"reason":   reasonAnonymizeFailed,
				"error":    lastAnonymizeErr.Error(),
				"contents": strconv.Itoa(anonymizeFailures),
				"language": language,
			},
		})
	}

	return &proto.PreRequestOutput{
		Allowed:              true,
		ModifiedMessagesJson: string(modifiedMessagesJSON),
		OutputsJson:          outputsJSON,
		NodeState:            stateJSON,
		// With nothing anonymized and no attachment removed, PostResponse has
		// nothing to restore and returns the response untouched. Saying so lets
		// the host stream the response live instead of buffering it whole.
		NoResponseRewrite: len(session.Mapping) == 0 && len(removedParts) == 0,
	}, nil
}

func (p *Plugin) PostResponse(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
	if len(in.NodeState) == 0 {
		return &proto.PostResponseOutput{}, nil
	}

	var state pluginState
	if err := json.Unmarshal(in.NodeState, &state); err != nil {
		return &proto.PostResponseOutput{}, nil
	}

	restored := deanonymize(in.ResponseContent, state.Mapping)

	slog.Debug("pseudonymizer: deanonymization done",
		slog.Int("entities", len(state.Mapping)),
		slog.Bool("modified", restored != in.ResponseContent),
		slog.Int("removed_attachments", len(state.RemovedParts)),
	)

	if len(state.RemovedParts) > 0 {
		restored = removedPartsWarning(state.RemovedParts) + restored
	}

	return &proto.PostResponseOutput{ModifiedResponseContent: restored}, nil
}

// injectPlaceholderInstruction prepends an instruction to the conversation's
// system message (or inserts a new one) telling the LLM to keep the
// pseudonymization placeholder tokens verbatim, so they can be restored in
// PostResponse. The instruction is written in language, the language resolved
// for the conversation. Returns messages unchanged if there is nothing to
// instruct about (no entities anonymized, or the strategy doesn't produce
// stable reusable tokens).
func injectPlaceholderInstruction(messages []map[string]any, mapping map[string]string, cfg Config, language string) []map[string]any {
	if !cfg.InjectInstruction || len(mapping) == 0 {
		return messages
	}
	if cfg.Strategy == "redact" {
		// Redacted blocks (████) carry no identifying token to preserve.
		return messages
	}

	instruction := buildInstructionText(mapping, language)

	for i, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "system" {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			// Non-string system content (parts array): insert a new system message instead.
			break
		}
		updated := make(map[string]any, len(msg))
		maps.Copy(updated, msg)
		updated["content"] = instruction + "\n\n" + content
		messages[i] = updated
		return messages
	}

	systemMsg := map[string]any{"role": "system", "content": instruction}
	return append([]map[string]any{systemMsg}, messages...)
}

// buildInstructionText builds the system instruction listing the placeholder
// tokens present in mapping, in the conversation's language.
func buildInstructionText(mapping map[string]string, language string) string {
	placeholders := make([]string, 0, len(mapping))
	for placeholder := range mapping {
		placeholders = append(placeholders, placeholder)
	}
	sort.Strings(placeholders)
	examples := strings.Join(placeholders, ", ")

	switch language {
	case "en":
		return fmt.Sprintf(
			"IMPORTANT: some personal or sensitive information in this conversation has been replaced "+
				"by placeholder tokens such as %s. These tokens will be automatically restored after your "+
				"reply. You MUST reproduce these tokens exactly as written (same brackets, same case, same "+
				"numbering), without translating, modifying, merging, splitting, or inventing new ones.",
			examples,
		)
	case "es":
		return fmt.Sprintf(
			"IMPORTANTE: algunas informaciones personales o sensibles de esta conversación han sido "+
				"reemplazadas por marcadores como %s. Estos marcadores se restaurarán automáticamente "+
				"después de tu respuesta. DEBES reproducirlos exactamente tal cual (mismos corchetes, "+
				"mismas mayúsculas, misma numeración), sin traducirlos, modificarlos, fusionarlos, "+
				"dividirlos ni inventar otros nuevos.",
			examples,
		)
	}

	return fmt.Sprintf(
		"IMPORTANT : certaines informations personnelles ou sensibles de cette conversation ont été "+
			"remplacées par des jetons de substitution tels que %s. Ces jetons seront automatiquement "+
			"restitués après ta réponse. Tu DOIS recopier ces jetons strictement à l'identique (mêmes "+
			"crochets, même casse, même numérotation), sans les traduire, les modifier, les fusionner, "+
			"les scinder, ni en inventer de nouveaux.",
		examples,
	)
}

// handleVerificationError convertit une erreur d'anonymisation en décision
// plugin. Si l'erreur n'est pas une *VerificationError, retourne nil (à
// traiter comme une autre erreur par l'appelant).
func handleVerificationError(in *proto.PreRequestInput, err error, cfg Config, host pluginsdk.HostClient) *proto.PreRequestOutput {
	var verr *anonymizer.VerificationError
	if !errors.As(err, &verr) {
		return nil
	}
	emitLeakEvent(in, verr, host)

	if cfg.VerificationOnLeak == "block" {
		return &proto.PreRequestOutput{Allowed: false}
	}
	return passthroughOutput()
}

// emitBlockedAttachmentsEvent signale le refus d'une requête portant des
// pièces jointes non pseudonymisables. Comme les autres événements, il ne
// transporte que des noms de fichiers et des motifs, jamais de contenu.
func (p *Plugin) emitBlockedAttachmentsEvent(in *proto.PreRequestInput, parts []removedPart) {
	reasons := make([]string, 0, len(parts))
	for _, part := range parts {
		reasons = append(reasons, part.Reason)
	}
	p.emitEvent(pluginsdk.Event{
		PluginName: "pseudonymizer",
		OrgID:      in.GetCtx().GetOrgId(),
		UserID:     in.GetCtx().GetUserId(),
		Type:       "attachment.blocked",
		Severity:   "warning",
		Message:    fmt.Sprintf("Requête refusée : %d pièce(s) jointe(s) non pseudonymisable(s)", len(parts)),
		Attributes: map[string]string{
			"attachments": strconv.Itoa(len(parts)),
			"reasons":     strings.Join(reasons, ", "),
		},
	})
}

// emitLeakEvent publie un événement décrivant la fuite détectée. Le rapport
// est sérialisé sans texte source (offsets et types seulement).
func emitLeakEvent(in *proto.PreRequestInput, verr *anonymizer.VerificationError, host pluginsdk.HostClient) {
	if host == nil {
		return
	}
	counts := verr.Report.CountByKind()
	attrs := map[string]string{
		"leak_count": strconv.Itoa(len(verr.Report.Leaks)),
	}
	for k, n := range counts {
		attrs["leak_"+k.String()] = strconv.Itoa(n)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.EmitEvent(ctx, pluginsdk.Event{
			PluginName: "pseudonymizer",
			OrgID:      in.GetCtx().GetOrgId(),
			UserID:     in.GetCtx().GetUserId(),
			Type:       "sensitive-data.leak",
			Severity:   "error",
			Message:    fmt.Sprintf("Fuite(s) PII détectée(s) après anonymisation : %d", len(verr.Report.Leaks)),
			Attributes: attrs,
		}); err != nil {
			slog.Warn("pseudonymizer: could not emit leak event", slog.Any("error", err))
		}
	}()
}

// pluginState is the opaque blob stored in node_state between PreRequest and PostResponse.
type pluginState struct {
	Mapping      map[string]string `json:"mapping"`
	RemovedParts []removedPart     `json:"removed_parts,omitempty"`
}

// removedPart describes a content part that was stripped from the request
// because the plugin cannot anonymize it.
type removedPart struct {
	Role   string `json:"role"`
	Type   string `json:"type"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Reasons an attachment could not be pseudonymized. They are shown to the end
// user, so they say what happened, never what the file contained.
const (
	reasonDisabled        = "traitement des pièces jointes désactivé"
	reasonNoInlineData    = "fichier transmis par référence, contenu illisible pour le filtre"
	reasonUnsupported     = "format non pris en charge"
	reasonNoText          = "aucun texte extractible (document scanné ou vide)"
	reasonTooLarge        = "fichier trop volumineux"
	reasonUnreadable      = "fichier illisible ou corrompu"
	reasonAnonymizeFailed = "échec de la pseudonymisation du contenu"
	reasonNonTextToolPart = "bloc d'outil au contenu non textuel"
)

// attachmentText resolves the text of an attachment to send in place of the
// file, or an error stating why it cannot be pseudonymized.
func attachmentText(cfg Config, att attachment) (text string, truncated bool, err error) {
	if !cfg.ProcessAttachments {
		return "", false, errors.New(reasonDisabled)
	}
	if att.Oversized {
		return "", false, fmt.Errorf("%w: above %d bytes", errTooLarge, cfg.MaxAttachmentBytes)
	}
	if len(att.Data) == 0 {
		return "", false, errors.New(reasonNoInlineData)
	}
	maxChars := cfg.MaxAttachmentChars
	if maxChars <= 0 {
		maxChars = defaultMaxAttachmentChars
	}
	return extractText(att, cfg.MaxAttachmentBytes, maxChars)
}

// attachmentReason maps an extraction failure to the wording shown to the user.
func attachmentReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNoText):
		return reasonNoText
	case errors.Is(err, errTooLarge):
		return reasonTooLarge
	case strings.HasPrefix(err.Error(), "unsupported format"):
		return reasonUnsupported
	case strings.Contains(err.Error(), reasonDisabled),
		strings.Contains(err.Error(), reasonNoInlineData):
		return err.Error()
	default:
		return reasonUnreadable
	}
}

// attachmentTextPart wraps the pseudonymized text of a document in a text part,
// labelled so the LLM knows it is reading an attachment rather than a message,
// and told when the text is only the beginning of it.
func attachmentTextPart(att attachment, partType, text string, truncated bool) string {
	name := att.Name
	if name == "" {
		name = "sans nom"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Contenu de la pièce jointe « %s »", name)
	if att.MediaType != "" {
		fmt.Fprintf(&b, ", type %s", att.MediaType)
	} else if partType != "" {
		fmt.Fprintf(&b, ", part %s", partType)
	}
	b.WriteString(", pseudonymisé")
	if truncated {
		b.WriteString(", tronqué")
	}
	b.WriteString("]\n")
	b.WriteString(text)
	if truncated {
		b.WriteString("\n[…] (document tronqué : la suite n'a pas été transmise)")
	}
	return b.String()
}

// blockedAttachmentsReason builds the rejection message listing the
// attachments that made the request fail.
func blockedAttachmentsReason(parts []removedPart) string {
	var b strings.Builder
	b.WriteString("Requête refusée par le pseudonymiseur : ")
	if len(parts) == 1 {
		b.WriteString("une pièce jointe ne peut pas être pseudonymisée")
	} else {
		fmt.Fprintf(&b, "%d pièces jointes ne peuvent pas être pseudonymisées", len(parts))
	}
	b.WriteString(" et ne peut donc pas être transmise au modèle.\n")
	for _, p := range parts {
		name := p.Name
		if name == "" {
			name = "pièce jointe sans nom"
		}
		fmt.Fprintf(&b, "- %s", name)
		if p.Reason != "" {
			fmt.Fprintf(&b, " : %s", p.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// partName extracts a human-readable name from a content part map, checking
// common fields used by various providers (Anthropic, OpenAI…).
func partName(partMap map[string]any) string {
	for _, key := range []string{"title", "name", "filename", "file_id"} {
		if v, ok := partMap[key].(string); ok && v != "" {
			return v
		}
	}
	// Nested source object (Anthropic document format).
	if src, ok := partMap["source"].(map[string]any); ok {
		for _, key := range []string{"name", "filename"} {
			if v, ok := src[key].(string); ok && v != "" {
				return v
			}
		}
	}
	// Nested file object (OpenAI file format).
	if file, ok := partMap["file"].(map[string]any); ok {
		for _, key := range []string{"name", "filename", "file_id"} {
			if v, ok := file[key].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// removedPartsWarning builds a markdown warning block to prepend to the
// LLM response when attachments were stripped from the request.
func removedPartsWarning(parts []removedPart) string {
	var b strings.Builder
	b.WriteString("> ⚠️ **Avertissement pseudonymiseur** : les pièces jointes suivantes n'ont pas pu être traitées par le filtre d'anonymisation et ont été automatiquement retirées de la requête :\n")
	for _, p := range parts {
		if p.Name != "" {
			fmt.Fprintf(&b, "> - **%s** (type : `%s`", p.Name, p.Type)
		} else {
			fmt.Fprintf(&b, "> - *pièce jointe sans nom* (type : `%s`", p.Type)
		}
		if p.Role != "" {
			fmt.Fprintf(&b, ", rôle : %s", p.Role)
		}
		if p.Reason != "" {
			fmt.Fprintf(&b, ", motif : %s", p.Reason)
		}
		b.WriteString(")\n")
	}
	b.WriteString("\n")
	return b.String()
}

// deanonymize replaces all placeholders in text with their original values from mapping.
func deanonymize(text string, mapping map[string]string) string {
	result := text
	for placeholder, original := range mapping {
		result = strings.ReplaceAll(result, placeholder, original)
	}
	return result
}

// getAnonymizer returns (creating if needed) the Anonymizer for cfg.Language.
// Thread-safe; recreates the store and anonymizers when config changes.
func (p *Plugin) getAnonymizer(ctx context.Context, cfg Config, language string) (*anonymizer.Anonymizer, error) {
	if _, err := p.ensureStore(cfg); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if anon, ok := p.anons[language]; ok {
		return anon, nil
	}

	anon, err := p.buildAnonymizer(ctx, cfg, language)
	if err != nil {
		return nil, err
	}
	p.anons[language] = anon
	return anon, nil
}

// resolveAnonymizer determines the language of the request — either the one
// pinned in the config, or the one detected on the raw message text — and
// returns the matching anonymizer. When a detected language cannot be served
// (missing model, download failure…), it falls back to the configured fallback
// language rather than letting the request through unpseudonymized.
func (p *Plugin) resolveAnonymizer(ctx context.Context, cfg Config, messages []map[string]any) (string, *anonymizer.Anonymizer, error) {
	fallback := cfg.FallbackLanguage
	if fallback == "" {
		fallback = defaultLanguage
	}

	language := cfg.Language
	detected := false
	if language == "" || language == LanguageAuto {
		detector, candidates, err := p.getDetector(ctx, cfg)
		if err != nil {
			return "", nil, err
		}
		language, detected = detectLanguage(ctx, detector, detectionSample(messages, maxDetectionSample), candidates, fallback)
	}

	anon, err := p.getAnonymizer(ctx, cfg, language)
	if err == nil {
		return language, anon, nil
	}

	if !detected || language == fallback {
		return "", nil, err
	}

	slog.WarnContext(ctx, "pseudonymizer: no anonymizer for detected language, falling back",
		slog.String("detected", language),
		slog.String("fallback", fallback),
		slog.Any("error", err),
	)

	anon, fallbackErr := p.getAnonymizer(ctx, cfg, fallback)
	if fallbackErr != nil {
		return "", nil, fallbackErr
	}
	return fallback, anon, nil
}

// ensureStore returns the model store for cfg, recreating it (and dropping the
// cached anonymizers and language detector) whenever the config changes.
func (p *Plugin) ensureStore(cfg Config) (*modelstore.Store, error) {
	cfgKey := buildCfgKey(cfg)

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.store != nil && p.lastCfgKey == cfgKey {
		return p.store, nil
	}

	store, err := modelstore.New(storeOptionsFromConfig(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("create model store: %w", err)
	}

	p.store = store
	p.anons = make(map[string]*anonymizer.Anonymizer)
	p.detector = nil
	p.candidates = nil
	p.lastCfgKey = cfgKey

	return p.store, nil
}

// getDetector returns the language detector and the languages it is restricted
// to. Both are built once per config: restricting the detector to the languages
// actually servable by the model store makes detection far more reliable on
// short texts.
func (p *Plugin) getDetector(ctx context.Context, cfg Config) (goanon.LanguageDetector, []string, error) {
	if _, err := p.ensureStore(cfg); err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.detector == nil {
		p.candidates = supportedLanguages(ctx, p.store)
		p.detector = goanon.NewWhatlangDetector(p.candidates...)
		slog.DebugContext(ctx, "pseudonymizer: language detector ready",
			slog.Any("candidates", p.candidates),
		)
	}

	return p.detector, p.candidates, nil
}

// buildAnonymizer downloads the model and creates a new Anonymizer for language.
// Must be called with p.mu held.
func (p *Plugin) buildAnonymizer(ctx context.Context, cfg Config, language string) (*anonymizer.Anonymizer, error) {
	modelPath, err := p.store.Get(ctx, language)
	if err != nil {
		return nil, fmt.Errorf("get model for language %q: %w", language, err)
	}

	f, err := os.Open(modelPath)
	if err != nil {
		return nil, fmt.Errorf("open model file: %w", err)
	}
	defer f.Close()

	model, err := goanon.LoadModel(f)
	if err != nil {
		return nil, fmt.Errorf("load model: %w", err)
	}

	// Load gazetteers (may be empty if not available).
	gazs, _ := p.store.GetGazetteers(ctx, language)
	loadedGazs, firstnamesGaz := loadGazetteers(ctx, gazs)

	// Build recognizer options in the correct filter application order.
	var recOpts []goanon.RecognizerOption
	recOpts = append(recOpts, goanon.WithLanguage(language))

	if cfg.BuiltinRegexPatterns {
		recOpts = append(recOpts, goanon.WithRegexPatterns(builtinRegexPatterns(cfg)...))
	}
	if cfg.BuiltinSecretPatterns {
		recOpts = append(recOpts, goanon.WithBuiltinSecretPatterns())
	}
	if len(loadedGazs) > 0 {
		recOpts = append(recOpts, goanon.WithGazetteers(loadedGazs))
	}
	if clusters := loadClusters(ctx, p.store, language); clusters != nil {
		recOpts = append(recOpts, goanon.WithBrownClusters(clusters))
	}

	if filters := postFilters(cfg); len(filters) > 0 {
		recOpts = append(recOpts, goanon.WithPostFilters(filters...))
	}
	if cfg.FirstNameReclassify && firstnamesGaz != nil {
		recOpts = append(recOpts, goanon.WithFirstNameReclassify(firstnamesGaz))
	}
	if cfg.Merge {
		recOpts = append(recOpts, goanon.WithMergePass())
	}
	if cfg.NameCompletion && firstnamesGaz != nil {
		recOpts = append(recOpts, goanon.WithNameCompletionPass(firstnamesGaz))
	}
	// Mirror server behaviour: WithFirstNameDetectionPass is applied alongside
	// WithFirstNameReclassify to detect first names not covered by the NER model.
	if cfg.FirstNameReclassify && firstnamesGaz != nil {
		recOpts = append(recOpts, goanon.WithFirstNameDetectionPass(firstnamesGaz))
	}

	rec, err := goanon.NewRecognizer(model, recOpts...)
	if err != nil {
		return nil, fmt.Errorf("create recognizer: %w", err)
	}

	// Warnings report mismatches between the model's training configuration and
	// the inference one (missing gazetteers, different language…). Each one
	// silently degrades detection quality, so surface them.
	for _, warning := range rec.Warnings() {
		slog.WarnContext(ctx, "pseudonymizer: recognizer configuration mismatch",
			slog.String("language", language),
			slog.String("warning", warning),
		)
	}

	// ConsistentMap is always true, matching the go-anon server behaviour.
	anonCfg := goanon.Config{
		Strategy:      strategyFromString(cfg.Strategy),
		ConsistentMap: true,
	}

	if len(cfg.SkipTypes) > 0 {
		skipSet := make(map[goanon.EntityType]bool, len(cfg.SkipTypes))
		for _, t := range cfg.SkipTypes {
			skipSet[goanon.EntityType(t)] = true
		}
		for _, t := range allEntityTypes {
			if !skipSet[t] {
				anonCfg.EntityTypes = append(anonCfg.EntityTypes, t)
			}
		}
	}

	return goanon.NewAnonymizer(rec, anonCfg), nil
}

// postFilters builds the entity filters pruning the recognizer output, in
// application order: confidence first, then span size, then blocklists.
func postFilters(cfg Config) []goanon.EntityFilter {
	var filters []goanon.EntityFilter
	if cfg.MinConfidence > 0 {
		filters = append(filters, goanon.MinConfidenceFilter(cfg.MinConfidence))
	}
	if cfg.MinRunes > 0 {
		filters = append(filters, ner.MinRunesFilter(cfg.MinRunes))
	}
	if cfg.MaxTokens > 0 {
		filters = append(filters, goanon.MaxTokensFilter(cfg.MaxTokens))
	}
	for typeStr, words := range cfg.Blocklist {
		if len(words) > 0 {
			filters = append(filters, goanon.BlocklistFilter(goanon.EntityType(typeStr), words...))
		}
	}
	return filters
}

// builtinRegexPatterns returns the builtin regex patterns to feed the
// recognizer with. IBAN, SIRET and SIREN are already validated against their
// control key by go-anon, which is what keeps a nine-digit reference number
// from being mistaken for a SIREN. SirenContextual goes one step further and
// swaps the SIREN pattern for the variant that also demands a textual marker
// upstream of the number.
func builtinRegexPatterns(cfg Config) []goanon.RegexPattern {
	patterns := make([]goanon.RegexPattern, 0, len(goanon.BuiltinRegexPatterns))
	for _, p := range goanon.BuiltinRegexPatterns {
		if cfg.SirenContextual && p.EntityType == goanon.TypeSIREN {
			p = ner.SIRENContextualPattern
		}
		patterns = append(patterns, p)
	}
	return patterns
}

// loadClusters fetches the Brown clusters published for language, if any.
//
// Missing clusters are not an error: a model published before the store
// started distributing them still works, only with a degraded feature set —
// Recognizer.Warnings() then reports the mismatch, which buildAnonymizer
// already relays.
func loadClusters(ctx context.Context, store *modelstore.Store, language string) *goanon.BrownClusters {
	path, err := store.GetClusters(ctx, language)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer: failed to get brown clusters",
			slog.String("language", language),
			slog.Any("error", err),
		)
		return nil
	}
	if path == "" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer: failed to open brown clusters", slog.Any("error", err))
		return nil
	}
	defer f.Close()

	clusters, err := goanon.LoadBrownClusters(f)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer: failed to load brown clusters", slog.Any("error", err))
		return nil
	}
	return clusters
}

// loadGazetteers opens and parses gazetteer files from the given path map.
func loadGazetteers(ctx context.Context, paths map[string]string) (map[string]*goanon.Gazetteer, *goanon.Gazetteer) {
	gazs := make(map[string]*goanon.Gazetteer, len(paths))
	var firstnamesGaz *goanon.Gazetteer
	for name, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			slog.WarnContext(ctx, "pseudonymizer: failed to open gazetteer", slog.String("name", name), slog.Any("error", err))
			continue
		}
		g, err := goanon.LoadGazetteer(name, f)
		f.Close()
		if err != nil {
			slog.WarnContext(ctx, "pseudonymizer: failed to load gazetteer", slog.String("name", name), slog.Any("error", err))
			continue
		}
		gazs[name] = g
		if name == "firstnames" {
			firstnamesGaz = g
		}
	}
	return gazs, firstnamesGaz
}

// storeOptionsFromConfig builds modelstore.Option list from Config.
func storeOptionsFromConfig(cfg Config) []modelstore.Option {
	var opts []modelstore.Option
	if cfg.CacheDir != "" {
		opts = append(opts, modelstore.WithCacheDir(cfg.CacheDir))
	}
	if cfg.ManifestURL != "" {
		opts = append(opts, modelstore.WithManifestURL(cfg.ManifestURL))
	}
	if cfg.Offline {
		opts = append(opts, modelstore.WithOfflineMode(true))
	}
	return opts
}

// buildCfgKey returns a string that changes whenever the config changes in a way
// that requires recreating the store or the anonymizers.
func buildCfgKey(cfg Config) string {
	b, _ := json.Marshal(cfg)
	return string(b)
}
