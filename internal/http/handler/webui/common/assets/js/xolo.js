// Xolo — le seul script maison de l'interface. Chaque fonction ci-dessous doit
// justifier son existence : tout ce qui peut être fait en HTMX ou par un script
// templui l'est.
//
// Tous les gestionnaires sont délégués depuis `document`. C'est ce qui les fait
// survivre aux échanges `hx-boost` : #content est remplacé à chaque navigation
// et <head> n'est jamais ré-exécuté, donc un écouteur posé sur un nœud de la
// page disparaîtrait avec lui.
(function () {
  "use strict";

  // ── Réponses HTTP échangées par HTMX ──────────────────────────────────────

  // Par défaut HTMX n'échange pas le corps d'une réponse 4xx/5xx. Les pages
  // d'erreur du produit (common.HandleError, 403, 404) sont rendues comme des
  // pages complètes avec leur propre statut : sans ceci, une navigation boostée
  // vers une ressource interdite ne montrerait rien du tout.
  //
  // La configuration est lue à chaque réponse, donc la poser depuis un script
  // `defer` — exécuté avant DOMContentLoaded, où htmx s'initialise — arrive à
  // temps.
  if (window.htmx) {
    window.htmx.config.responseHandling = [
      // 204 No Content ne remplace rien, et n'est pas une erreur.
      { code: "204", swap: false },
      { code: "[23]..", swap: true },
      { code: "[45]..", swap: true, error: true },
    ];
  }

  // ── Surfaces flottantes ───────────────────────────────────────────────────

  // Ferme les surfaces flottantes avant un échange HTMX qui se produit *hors*
  // d'elles.
  //
  // popover.min.js déplace le contenu d'un popover dans un portail attaché à
  // <body> et y installe une boucle requestAnimationFrame de repositionnement.
  // Une navigation `hx-boost` déclenchée depuis l'intérieur du popover (le
  // sélecteur de contexte) échange la page sous lui : sans ceci il resterait
  // ouvert par-dessus la nouvelle page, et sa boucle rAF continuerait de tourner
  // sur un nœud détaché à chaque navigation. `close` passe par l'API du
  // composant, qui exécute le nettoyage de `autoUpdate`.
  //
  // Un popover qui contient lui-même la cible de l'échange est épargné : c'est
  // le cas de la recherche du sélecteur de contexte, qui ne remplace que sa
  // propre liste de résultats. Le fermer reviendrait à faire disparaître le
  // champ sous les doigts de l'utilisateur à la première lettre saisie.
  function attrValue(value) {
    return value.replace(/["\\]/g, "\\$&");
  }

  function portalContainer() {
    return document.querySelector("[data-tui-popover-portal-container]");
  }

  function portaledSurfaces() {
    var portal = portalContainer();
    return portal ? portal.querySelectorAll("[data-tui-popover-id]") : [];
  }

  function triggerOf(surface) {
    var id = surface.getAttribute("data-tui-popover-id");
    if (!id) return null;
    return document.querySelector('[data-tui-popover-trigger="' + attrValue(id) + '"]');
  }

  // Marque, *avant* l'échange, les surfaces dont la page d'origine part avec lui.
  //
  // Fermer ne suffit pas : `open` **déplace** le contenu du popover dans le
  // portail et `close` ne l'en ressort jamais. Le nœud survit donc à l'échange
  // qui détruit la page dont il vient, et comme les identifiants sont rendus par
  // le serveur (`role-filter-content`, `usage-filters`…), la page suivante en
  // rend un jumeau : deux éléments portent le même id.
  //
  // Le doublon ne se voit pas tout de suite. `document.getElementById` rend le
  // premier dans l'ordre du document, donc le neuf — jusqu'à la réouverture, qui
  // le déplace à la fin du portail, *derrière* l'ancien. À partir de là toutes
  // les recherches par id du composant tombent sur le nœud mort : le selectbox
  // relit une liste vide (le libellé retombe sur le placeholder, la coche reste
  // sur l'élément cliqué) et `closePopover` masque un nœud déjà invisible — la
  // surface visible ne se ferme plus. C'est le symptôme « après quelques clics,
  // le filtre ne se ferme ni ne sélectionne plus ».
  //
  // Le critère est le déclencheur, pas la surface : une surface dont le
  // déclencheur est dans la région échangée voit sa page disparaître, et le
  // serveur en rend forcément un exemplaire neuf dans la réponse. Rien d'autre
  // n'est retiré — surtout pas sur un simple « plus aucun déclencheur ne pointe
  // vers cet id ». Les surfaces de la coquille (#context-switcher) ne vivent pas
  // dans #content : elles ne sont jamais re-rendues, seulement rafraîchies hors
  // bande, et hors bande ne sait que *remplacer* une cible existante. En
  // supprimer une serait sans retour — le sélecteur de contexte resterait mort
  // jusqu'au prochain rechargement complet.
  //
  // Le marquage se répète tant qu'il progresse : un panneau condamné emporte le
  // déclencheur des surfaces qu'il hébergeait (le selectbox du panneau de
  // filtres de l'usage), qui ne deviennent condamnées qu'au tour suivant.
  var DOOMED = "data-xolo-portal-doomed";

  function markDoomed(swapped) {
    if (!swapped) return;
    var progressed;
    do {
      progressed = false;
      Array.prototype.forEach.call(portaledSurfaces(), function (surface) {
        if (surface.hasAttribute(DOOMED)) return;
        var trigger = triggerOf(surface);
        if (!trigger) return;
        var doomedHost = trigger.closest("[" + DOOMED + "]");
        if (!swapped.contains(trigger) && !doomedHost) return;
        surface.setAttribute(DOOMED, "");
        progressed = true;
      });
    } while (progressed);
  }

  // Retire du portail ce qui est condamné, ou ce dont le remplaçant est déjà
  // visible ailleurs dans le document — deux cas où la suppression ne peut pas
  // laisser l'interface sans surface.
  function purgePortal() {
    var portal = portalContainer();
    if (!portal) return;
    Array.prototype.forEach.call(portaledSurfaces(), function (surface) {
      var id = surface.getAttribute("data-tui-popover-id");
      if (!id) return;

      var stale = surface.hasAttribute(DOOMED);
      if (!stale) {
        document
          .querySelectorAll('[data-tui-popover-id="' + attrValue(id) + '"]')
          .forEach(function (twin) {
            if (twin !== surface && !portal.contains(twin)) stale = true;
          });
      }
      if (!stale) return;

      // Passe par l'API du composant tant que le nœud est encore là : c'est elle
      // qui libère la boucle de repositionnement enregistrée sous cet id.
      if (window.tui && window.tui.popover) window.tui.popover.close(id, true);
      surface.remove();
    });
  }

  document.addEventListener("htmx:beforeSwap", function (event) {
    var swapped = event.detail && event.detail.target;
    if (window.tui && window.tui.popover) {
      document
        .querySelectorAll('[data-tui-popover-open="true"][data-tui-popover-id]')
        .forEach(function (surface) {
          if (swapped && surface.contains(swapped)) return;
          window.tui.popover.close(surface.id);
        });
    }
    markDoomed(swapped);
  });

  document.addEventListener("htmx:afterSwap", purgePortal);

  // La restauration d'historique ne passe par aucun échange : HTMX réécrit le
  // `innerHTML` de <body> — portail compris — depuis l'instantané pris avant la
  // navigation. Les surfaces qu'il contenait reviennent donc en double avec
  // celles du #content restauré, et c'est le doublon visible qui les départage.
  // Les fermer d'abord remet à zéro l'état ouvert figé dans l'instantané.
  document.addEventListener("htmx:historyRestore", function () {
    if (window.tui && window.tui.popover) window.tui.popover.closeAll();
    purgePortal();
  });

  // ── Soumission au changement ──────────────────────────────────────────────

  // Un contrôle qui pilote un filtre de page (période d'usage, portée…) doit
  // recharger dès qu'il change. `requestSubmit` déclenche la validation native
  // et l'événement `submit`, contrairement à `form.submit()` — c'est ce qui
  // laisse HTMX intercepter la navigation via `hx-boost`.
  document.addEventListener("change", function (event) {
    var el = event.target.closest("[data-xolo-submit-on-change]");
    if (!el || !el.form) return;
    el.form.requestSubmit();
  });

  // ── Déclencheur de champ fichier ──────────────────────────────────────────

  // Un `<input type=file>` ne peut pas prendre l'apparence d'un bouton du design
  // system. Le motif habituel — champ masqué + bouton qui le clique — est le
  // seul qui n'impose pas de réécrire le contrôle.
  document.addEventListener("click", function (event) {
    var trigger = event.target.closest("[data-xolo-file-trigger]");
    if (!trigger) return;
    var input = document.getElementById(trigger.dataset.xoloFileTrigger);
    if (input) input.click();
  });

  // ── Import d'un pipeline depuis un fichier ────────────────────────────────

  // Le fichier est lu dans le navigateur puis posté en JSON : l'API d'import
  // attend un corps JSON, pas un multipart, et aucune route ne fait la
  // conversion côté serveur. La redirection ne peut pas être un simple lien : la
  // cible dépend de l'identifiant renvoyé par l'import.
  document.addEventListener("change", async function (event) {
    var input = event.target.closest("[data-xolo-import]");
    if (!input) return;

    var file = input.files && input.files[0];
    if (!file) return;

    try {
      var bundle = JSON.parse(await file.text());
      var response = await fetch(input.dataset.xoloImport, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bundle),
      });
      if (!response.ok) {
        window.alert("Erreur à l'import : " + (await response.text()));
        return;
      }
      var created = await response.json();
      window.location.href = input.dataset.redirectBase + created.id + "/pipeline";
    } catch (err) {
      window.alert("Fichier invalide : " + err);
    } finally {
      input.value = "";
    }
  });

  // ── Listes de lignes répétables ───────────────────────────────────────────
  //
  // Deux éditeurs ajoutent et retirent des lignes de formulaire sans aller-retour
  // serveur : les attributs de corps de requête d'un modèle, et les contraintes
  // d'un plan d'abonnement. Les deux postent un tableau à plat
  // (`champ_0_clé`, `champ_1_clé`, …) accompagné d'un compteur, donc retirer une
  // ligne oblige à renuméroter les suivantes — c'est la seule raison d'être de
  // ce module.
  //
  // Le balisage attendu :
  //
  //   <div id="rows" data-xolo-rows
  //        data-rows-template="rows-tpl" data-rows-count="rows-count"> … </div>
  //   <input type="hidden" id="rows-count" name="…">
  //   <template id="rows-tpl"><div data-xolo-row> … </div></template>
  //   <button data-xolo-row-add="rows">…</button>
  //
  // et, dans chaque ligne, les champs à renuméroter portent
  // `data-xolo-name="prefixe_{i}_suffixe"`.

  function reindexRows(container) {
    var rows = container.querySelectorAll(":scope > [data-xolo-row]");
    rows.forEach(function (row, index) {
      row.querySelectorAll("[data-xolo-name]").forEach(function (field) {
        field.name = field.dataset.xoloName.replace(/\{i\}/g, index);
      });
      // Idem pour les identifiants : une ligne clonée depuis le <template> porte
      // l'id de la ligne modèle, et deux éléments de même id rendent le document
      // invalide — sans effet tant qu'aucun composant n'indexe par id, mais c'est
      // exactement le genre de dette qui casse silencieusement plus tard.
      row.querySelectorAll("[data-xolo-id]").forEach(function (el) {
        el.id = el.dataset.xoloId.replace(/\{i\}/g, index);
      });
      syncRowVariant(row);
    });

    var counter = document.getElementById(container.dataset.rowsCount);
    if (counter) counter.value = rows.length;
  }

  // syncRowVariant montre les champs propres au type choisi dans la ligne : une
  // contrainte « fenêtre glissante » et une contrainte « concurrence » n'ont pas
  // les mêmes champs, et le formulaire porte les deux jeux.
  function syncRowVariant(row) {
    var selector = row.querySelector("[data-xolo-row-variant]");
    if (!selector) return;
    row.querySelectorAll("[data-xolo-variant]").forEach(function (group) {
      var matches = selector.value !== "" && group.dataset.xoloVariant === selector.value;
      group.hidden = !matches;
    });
  }

  document.addEventListener("click", function (event) {
    var add = event.target.closest("[data-xolo-row-add]");
    if (add) {
      var container = document.getElementById(add.dataset.xoloRowAdd);
      var template = document.getElementById(container.dataset.rowsTemplate);
      container.appendChild(template.content.cloneNode(true));
      reindexRows(container);
      return;
    }

    var remove = event.target.closest("[data-xolo-row-remove]");
    if (remove) {
      var row = remove.closest("[data-xolo-row]");
      var owner = row.parentElement;
      row.remove();
      reindexRows(owner);
    }
  });

  document.addEventListener("change", function (event) {
    var selector = event.target.closest("[data-xolo-row-variant]");
    if (!selector) return;
    syncRowVariant(selector.closest("[data-xolo-row]"));
  });

  // Les lignes rendues par le serveur doivent partir dans le bon état : elles
  // n'ont jamais reçu l'événement `change` qui règle la visibilité.
  function initRows(root) {
    if (!root || !root.querySelectorAll) return;
    root.querySelectorAll("[data-xolo-rows] > [data-xolo-row]").forEach(syncRowVariant);
  }

  document.addEventListener("DOMContentLoaded", function () {
    initRows(document);
  });
  document.addEventListener("htmx:afterSwap", function (event) {
    initRows(event.target);
  });

  // ── Pré-remplissage depuis le catalogue models.dev ────────────────────────
  //
  // Le bouton interroge le catalogue public et remplit une dizaine de champs du
  // formulaire de modèle. Le faire côté serveur voudrait dire soumettre puis
  // re-rendre le formulaire, en perdant la saisie déjà en cours.
  document.addEventListener("click", async function (event) {
    var button = event.target.closest("[data-xolo-models-dev]");
    if (!button) return;

    var form = button.closest("form");
    var setValue = function (name, value) {
      var field = form.querySelector("[name=" + name + "]");
      if (field) field.value = value;
    };
    var setChecked = function (name, value) {
      var field = form.querySelector("[name=" + name + "]");
      if (field) field.checked = value;
    };

    var realModel = form.querySelector("[name=real_model]").value.trim();
    if (!realModel) {
      window.alert("Renseignez d'abord le nom du modèle réel.");
      return;
    }

    button.disabled = true;
    try {
      var url = button.dataset.lookupUrl + "?id=" + encodeURIComponent(realModel);
      if (button.dataset.providerType) {
        url += "&provider=" + encodeURIComponent(button.dataset.providerType);
      }

      var response = await fetch(url);
      if (!response.ok) {
        window.alert("Modèle non trouvé dans le catalogue models.dev.");
        return;
      }
      var catalog = await response.json();

      if (catalog.context_window) setValue("context_window", catalog.context_window);
      if (catalog.output_window) setValue("output_window", catalog.output_window);

      if (catalog.prompt_cost || catalog.completion_cost) {
        // Le catalogue publie ses tarifs en USD ; l'organisation les stocke dans
        // sa propre devise.
        var currency = button.dataset.currency || "USD";
        var rate = 1;
        if (currency !== "USD") {
          var rateResponse = await fetch(
            button.dataset.exchangeRateUrl + "?from=USD&to=" + encodeURIComponent(currency)
          );
          if (rateResponse.ok) rate = (await rateResponse.json()).rate;
        }
        if (catalog.prompt_cost) {
          setValue("prompt_cost", ((catalog.prompt_cost / 1000) * rate).toFixed(6));
        }
        if (catalog.completion_cost) {
          setValue("completion_cost", ((catalog.completion_cost / 1000) * rate).toFixed(6));
        }
      }

      setChecked("cap_tools", !!catalog.cap_tools);
      setChecked("cap_vision", !!catalog.cap_vision);
      setChecked("cap_reasoning", !!catalog.cap_reasoning);
      setChecked("cap_audio", !!catalog.cap_audio);
      setChecked("cap_embeddings", !!catalog.cap_embeddings);

      if (catalog.name) {
        var proxyName = form.querySelector("[name=proxy_name]");
        if (proxyName && !proxyName.value) proxyName.value = catalog.name;
      }
    } finally {
      button.disabled = false;
    }
  });
})();

// ── Éditeur de tableau d'objets (formulaires de plugin) ──────────────────────
//
// Un champ de schéma JSON de type « tableau d'objets » n'a pas de contrepartie
// HTML : il faut construire un sous-formulaire par élément, à partir d'un schéma
// connu seulement à l'exécution, et poster le tout dans un champ caché en JSON.
// C'est le seul écran du produit dont le balisage ne peut pas être rendu par le
// serveur — les schémas viennent des plugins.
//
// Le code vivait dans un `<script>` inline utilisant
// `document.currentScript.previousElementSibling`. Cela ne survit pas à
// `hx-boost` : les scripts d'un fragment échangé s'exécutent, mais
// `document.currentScript` y vaut null. Ici l'initialisation part du DOM, elle
// est idempotente, et elle est rejouée après chaque échange.
(function () {
  "use strict";

  var DAYS_FR = {
    monday: "Lundi",
    tuesday: "Mardi",
    wednesday: "Mercredi",
    thursday: "Jeudi",
    friday: "Vendredi",
    saturday: "Samedi",
    sunday: "Dimanche",
  };

  var INPUT_CLASS =
    "flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";
  var CHECKBOX_CLASS = "h-4 w-4 rounded border-input accent-primary";

  function parseJSON(raw, fallback) {
    if (!raw || raw === "null") return fallback;
    try {
      var parsed = JSON.parse(raw);
      return parsed === null ? fallback : parsed;
    } catch (err) {
      return fallback;
    }
  }

  function initArrayField(root) {
    if (root.dataset.xoloArrayReady === "1") return;
    root.dataset.xoloArrayReady = "1";

    var itemsEl = root.querySelector(".array-field-items");
    var hiddenInput = root.querySelector(".array-field-json");
    var addButton = root.querySelector(".array-field-add");

    var schema = parseJSON(root.dataset.schema, {});
    var props = schema.properties || {};
    // L'ordre des propriétés porte du sens : les obligatoires d'abord.
    var required = schema.required || [];
    var propKeys = required.concat(
      Object.keys(props).filter(function (key) {
        return required.indexOf(key) === -1;
      })
    );

    var items = parseJSON(root.dataset.items, []);
    if (!Array.isArray(items)) items = [];

    function fieldControl(propName, propSchema, currentValue, index) {
      if (propSchema.type === "array" && propSchema.items && propSchema.items.enum) {
        var group = document.createElement("div");
        group.className = "flex flex-wrap gap-x-4 gap-y-2 pt-1";
        var selected = Array.isArray(currentValue) ? currentValue : [];

        propSchema.items.enum.forEach(function (enumValue) {
          var label = document.createElement("label");
          label.className = "flex items-center gap-1.5 text-sm cursor-pointer";

          var box = document.createElement("input");
          box.type = "checkbox";
          box.value = enumValue;
          box.checked = selected.indexOf(enumValue) !== -1;
          box.dataset.prop = propName;
          box.className = CHECKBOX_CLASS;
          box.addEventListener("change", function () {
            syncItem(index);
          });

          var text = document.createElement("span");
          text.textContent = DAYS_FR[enumValue] || enumValue;

          label.appendChild(box);
          label.appendChild(text);
          group.appendChild(label);
        });
        return group;
      }

      if (propSchema.type === "boolean") {
        var toggle = document.createElement("input");
        toggle.type = "checkbox";
        toggle.checked = !!currentValue;
        toggle.dataset.prop = propName;
        toggle.className = CHECKBOX_CLASS;
        toggle.addEventListener("change", function () {
          syncItem(index);
        });
        return toggle;
      }

      var input = document.createElement("input");
      var description = (propSchema.description || "").toLowerCase();
      if (description.indexOf("hh:mm") !== -1 || propSchema.format === "time") {
        input.type = "time";
      } else if (propSchema.type === "number" || propSchema.type === "integer") {
        input.type = "number";
      } else {
        input.type = "text";
      }
      input.value = currentValue != null ? String(currentValue) : "";
      input.dataset.prop = propName;
      input.className = INPUT_CLASS;
      input.addEventListener("input", function () {
        syncItem(index);
      });
      return input;
    }

    function renderItem(index, item) {
      var row = document.createElement("div");
      row.className = "border border-border rounded-md p-4 flex flex-col gap-4";
      row.dataset.idx = index;

      var header = document.createElement("div");
      header.className = "flex justify-between items-center";

      var title = document.createElement("span");
      title.className = "text-sm font-semibold";
      title.textContent = "Élément #" + (index + 1);

      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "text-sm text-destructive hover:underline";
      remove.textContent = "Supprimer";
      remove.addEventListener("click", function () {
        syncAll();
        items.splice(index, 1);
        redraw();
      });

      header.appendChild(title);
      header.appendChild(remove);
      row.appendChild(header);

      propKeys.forEach(function (propName) {
        var propSchema = props[propName];
        if (!propSchema) return;

        var field = document.createElement("div");
        field.className = "flex flex-col gap-1";

        var label = document.createElement("label");
        label.className = "text-sm font-medium";
        label.textContent = propSchema.title || propName;
        field.appendChild(label);

        if (propSchema.description) {
          var hint = document.createElement("p");
          hint.className = "text-xs text-muted-foreground";
          hint.textContent = propSchema.description;
          field.appendChild(hint);
        }

        var currentValue =
          item && item[propName] !== undefined
            ? item[propName]
            : propSchema.default !== undefined
              ? propSchema.default
              : null;

        field.appendChild(fieldControl(propName, propSchema, currentValue, index));
        row.appendChild(field);
      });

      return row;
    }

    function collectItem(row) {
      var item = {};
      propKeys.forEach(function (propName) {
        var propSchema = props[propName];
        if (!propSchema) return;

        if (propSchema.type === "array") {
          var boxes = row.querySelectorAll('[data-prop="' + propName + '"]');
          item[propName] = Array.prototype.filter
            .call(boxes, function (box) {
              return box.checked;
            })
            .map(function (box) {
              return box.value;
            });
          return;
        }

        var control = row.querySelector('[data-prop="' + propName + '"]');
        if (propSchema.type === "boolean") {
          item[propName] = control ? control.checked : false;
        } else if (propSchema.type === "number" || propSchema.type === "integer") {
          if (control) {
            var parsed =
              propSchema.type === "integer"
                ? parseInt(control.value, 10)
                : parseFloat(control.value);
            item[propName] = isNaN(parsed) ? null : parsed;
          }
        } else {
          item[propName] = control ? control.value : "";
        }
      });
      return item;
    }

    function syncAll() {
      itemsEl.querySelectorAll("[data-idx]").forEach(function (row) {
        items[parseInt(row.dataset.idx, 10)] = collectItem(row);
      });
    }

    function syncItem(index) {
      var row = itemsEl.querySelector('[data-idx="' + index + '"]');
      if (row) items[index] = collectItem(row);
      serialize();
    }

    function serialize() {
      hiddenInput.value = JSON.stringify(items);
    }

    function redraw() {
      itemsEl.innerHTML = "";
      items.forEach(function (item, index) {
        itemsEl.appendChild(renderItem(index, item));
      });
      serialize();
    }

    if (addButton) {
      addButton.addEventListener("click", function () {
        syncAll();
        items.push({});
        redraw();
      });
    }

    // La capture garantit que la sérialisation précède tout autre gestionnaire
    // de soumission.
    var form = root.closest("form");
    if (form) {
      form.addEventListener(
        "submit",
        function () {
          syncAll();
          serialize();
        },
        true
      );
    }

    redraw();
  }

  function initArrayFields(root) {
    if (!root || !root.querySelectorAll) return;
    root.querySelectorAll(".array-field-root").forEach(initArrayField);
  }

  document.addEventListener("DOMContentLoaded", function () {
    initArrayFields(document);
  });
  document.addEventListener("htmx:afterSwap", function (event) {
    initArrayFields(event.target);
  });
})();
