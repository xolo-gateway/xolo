package main

import (
	"context"
	"errors"
	"fmt"

	goanon "github.com/bornholm/go-anon"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const secretKeyHashHMAC = "hash_key"

// hashKeyLoader est l'interface minimale dont buildAnonymizeOptions a besoin.
// pluginsdk.HostClient la satisfait ; la signature d'interface permet de
// stubber facilement le host dans les tests unitaires.
type hashKeyLoader interface {
	GetSecret(ctx context.Context, orgID, pluginName, nodeID, key string) (string, bool, error)
}

// errHashKeyMissing signale une stratégie hash sans clé HMAC exploitable. La
// requête est alors refusée (fail-closed) : la laisser passer enverrait les
// données personnelles en clair au modèle.
var errHashKeyMissing = errors.New("la stratégie hash requiert une clé HMAC configurée sur le nœud")

// buildAnonymizeOptions assemble les AnonymizeOption à passer à Anonymize()
// en fonction de la configuration et de l'état du store de secrets.
//
// Comportement :
//   - Verification stricte prime sur l'observation (WithStrictVerification
//     inclut la vérification, mais on reste explicite pour la lecture).
//   - La clé HMAC est chargée via le store de secrets de l'hôte ; sans clé
//     valide, la fonction échoue avec une erreur enveloppant errHashKeyMissing
//     et la requête doit être refusée.
//   - HashScope n'est appliqué que si la stratégie est hash.
func buildAnonymizeOptions(
	ctx context.Context,
	cfg Config,
	reqCtx *proto.RequestContext,
	host hashKeyLoader,
) ([]goanon.AnonymizeOption, error) {
	var opts []goanon.AnonymizeOption

	if cfg.VerificationStrict {
		opts = append(opts, goanon.WithStrictVerification())
	} else if cfg.Verification {
		opts = append(opts, goanon.WithVerification())
	}

	if cfg.Strategy == "hash" {
		if host == nil {
			return nil, fmt.Errorf("%w : store de secrets indisponible", errHashKeyMissing)
		}
		raw, found, err := host.GetSecret(ctx, reqCtx.GetOrgId(), "pseudonymizer", reqCtx.GetNodeId(), secretKeyHashHMAC)
		if err != nil {
			return nil, fmt.Errorf("%w : lecture du store de secrets : %v", errHashKeyMissing, err)
		}
		if !found || raw == "" {
			return nil, fmt.Errorf("%w : aucune clé enregistrée", errHashKeyMissing)
		}
		key, err := goanon.ParseHashKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%w : clé invalide : %v", errHashKeyMissing, err)
		}
		opts = append(opts, goanon.WithHashKey(key))
		if cfg.HashScope != "" {
			opts = append(opts, goanon.WithHashScope(cfg.HashScope))
		}
	}

	return opts, nil
}
