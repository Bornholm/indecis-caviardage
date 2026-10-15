# indecis-caviardage

Un démonstrateur de caviardage de données personnelles : on dépose un document Word ou OpenDocument, ou un fichier CSV, il en masque les données personnelles et liste ce qu'il a masqué. Tout tourne sur la machine, sur le processeur, en Go pur : le document n'est envoyé nulle part et n'est pas conservé.

La version tout navigateur est en ligne : https://bornholm.github.io/indecis-caviardage/

## Le modèle

Le modèle `work-fam` d'[indecis-ner](https://github.com/Bornholm/indecis-ner) est joint à ses releases. Les commandes `make` l'attendent dans `../indecis-ner/runs/work-fam`, ou dans le dossier donné par `MODEL` :

```bash
gh release download v0.1.0 --repo Bornholm/indecis-ner --dir model
make run MODEL=model
```

## Ce que le serveur garde, et combien de temps

- Le document reçu et le document produit sont écrits pendant le traitement dans un dossier de `/dev/shm`. Le formulaire est lu au fil de l'envoi : un gros fichier ne passe pas par le répertoire temporaire du système, contrairement au traitement standard des formulaires en Go, qui l'écrirait sur disque au-delà de 32 Mo, en mémoire, jamais sur disque quand ce répertoire existe ; le dossier est supprimé à la fin de la requête, et au démarrage si un arrêt brutal l'avait laissé.
- Le document caviardé attend son téléchargement en mémoire, au plus 15 minutes. Il est supprimé dès le premier téléchargement, ou à son expiration (purge chaque minute), ses octets effacés.
- La liste des éléments masqués, qui contient les valeurs d'origine, est envoyée au navigateur pour l'affichage ; le serveur ne la garde pas.
- Les journaux ne portent que le format, la taille, le nombre d'éléments et la durée, jamais de contenu.
- Go ne remet pas à zéro la mémoire qu'il libère : des fragments peuvent subsister dans le processus tant qu'elle n'est pas réutilisée. Arrêter le serveur après usage les efface.

```bash
make run    # http://127.0.0.1:8071
```

1. Déposer un fichier `.docx`, `.odt`, `.csv` ou `.tsv`.
2. Régler la **prudence** : plus elle est haute, moins il reste de données lisibles, et plus il y a de masques superflus. À 5, environ 2 % des passages personnels restaient exposés dans nos tests en prose ; à 6, environ 1 %.
3. Choisir entre le **caviardage** (██████, irréversible) et la **pseudonymisation** (⟦PERSON_1⟧ : une même personne garde le même pseudonyme dans tout le document).
4. Télécharger le document produit, et parcourir la liste des éléments masqués : type, contexte, nombre d'occurrences, et catégorie sensible (santé, syndicat, convictions, origine, opinions politiques, vie sexuelle, infractions).

## Version tout navigateur

```bash
make serve  # produit dist/ et le sert sur http://127.0.0.1:8073
```

`make wasm` produit dans `dist/` la même page, avec le même traitement compilé en WebAssembly : le document ne quitte pas le navigateur. `dist/` ne contient que des fichiers statiques (environ 340 Mo, le modèle compris), à servir avec n'importe quel serveur web.

- Le modèle est téléchargé au premier chargement de la page, puis gardé dans le cache du navigateur tant que sa version ne change pas. Ce cache demande https ou localhost, et le navigateur peut le refuser faute de place : le modèle est alors téléchargé à chaque visite.
- Le calcul tourne dans un Web Worker, sur un seul cœur : le WebAssembly de Go n'a pas de threads. Il faut compter environ 1 Go de mémoire pour l'onglet.
- Le panneau « Ce navigateur » indique si le calcul profite des instructions vectorielles du processeur, par WebAssembly SIMD (128 bits). Sans elles, la page charge un second module compilé sans SIMD, environ dix fois plus lent.
- Le document de `demo/` se traite en 3,1 s dans Chromium avec SIMD et en 28,5 s sans, contre 0,3 s avec le serveur (int8 et assembleur natifs).

## Fonctionnement

Un modèle [indecis](https://github.com/Bornholm/indecis) de 7,7 millions de paramètres, entraîné dans [indecis-ner](https://github.com/Bornholm/indecis-ner), lit le document en entier et repère les passages à masquer : identifiants directs (nom, NIR, courriel, téléphone, adresse), quasi-identifiants (métier, lieu, date, proches, événements de vie) et données sensibles de l'article 9. [go-anon](https://github.com/Bornholm/go-anon) réécrit le document dans son format, y repasse une détection pour vérifier, et en retire les métadonnées, commentaires et révisions ; ses expressions régulières complètent le modèle pour les identifiants (courriel, IBAN, téléphone, SIRET).

La liste vient de la table de correspondance de go-anon : elle donne exactement ce qui a été remplacé. Le traitement pseudonymise d'abord, puis, en mode caviardage, remplace les pseudonymes par des blocs.

Sur un portable (Core Ultra 7 265U, modèle int8), dix pages A4 se traitent en 1,3 s sur un cœur et environ 320 Mo de mémoire. Les mesures de qualité et leurs limites sont dans le README d'indecis-ner.

## Limites

- Le modèle masque large plutôt que de laisser fuir : des titres ou des mots courants sont parfois masqués. Un contrôle humain reste nécessaire avant toute diffusion.
- La catégorie sensible d'un passage est indicative : le masquage est fiable, la catégorisation beaucoup moins.
- go-anon réunit les lignes d'un même paragraphe séparées par des sauts de ligne simples : « Chalon-sur-Saône » et « Date » à la ligne suivante deviennent « Chalon-sur-SaôneDate ». Les paragraphes distincts ne sont pas concernés.
- Pas de PDF dans ce démonstrateur, bien que go-anon les traite.

## Fichiers CSV

Un fichier CSV ou TSV peut peser plusieurs dizaines de mégaoctets. Il est lu et écrit au fil de l'eau par le `csv.Stream` de go-anon, par lots d'environ 16 ko de texte : la mémoire ne dépend pas de la taille du fichier. `csv.go` y ajoute ce qui tient au modèle, qui lit sinon le document entier d'un seul tenant.

- Le séparateur (virgule, point-virgule, tabulation), le BOM et les fins de ligne CRLF sont conservés. Le guillemetage peut changer : Go ne met des guillemets qu'aux cellules qui en ont besoin.
- Le modèle lit chaque ligne comme des lignes « colonne : valeur ». Ainsi « 06 12 34 56 78 » se lit sous « téléphone », et pas seul. Il lit les lignes d'un lot ensemble, en fenêtres réparties sur les cœurs.
- La première ligne est prise pour un en-tête, gardée telle quelle, quand ses cellules sont courtes, distinctes et non vides, et qu'une colonne a des chiffres ou un « @ » dans la deuxième ligne mais pas dans la première (`csv.LooksLikeHeader` de go-anon). Sinon, elle est caviardée comme les autres : un en-tête sans colonne chiffrée est alors masqué, plutôt que de risquer de laisser passer une ligne de données.
- Chaque lot est découpé en quatre parts d'environ 4 ko que le modèle lit en même temps : sur un seul lot à la fois, il n'occupait qu'environ quatre cœurs sur quatorze. Le débit double ; au-delà de quatre parts, il ne gagne plus rien. Dans le navigateur, sur un seul fil, le lot reste d'un seul tenant.
- Une cellule qu'une expression régulière de go-anon reconnaît en entier (courriel, téléphone, IBAN, SIRET) est masquée sans passer par le modèle, qui l'aurait masquée aussi : ces identifiants lui coûtent beaucoup de tokens.
- Une cellule déjà lue sous la même colonne reprend ce qui y a été trouvé la première fois, sans repasser par le modèle (jusqu'à 200 000 cellules distinctes). Les villes, les catégories et les commentaires récurrents ne coûtent donc qu'une fois.
- La vérification se fait cellule par cellule : formes déjà remplacées, identifiants par expressions régulières. Le modèle ne relit pas le fichier produit, ce qui doublerait la durée.
- La page liste les 2 000 éléments les plus fréquents. Les comptes portent sur tout le fichier.
- La page affiche la progression et le temps restant. Côté serveur, ce sont les lignes JSON de `/api/redact?progress` ; sans `?progress`, la réponse reste un seul objet JSON.

Le débit dépend du modèle, qui lit chaque cellule. Sur le portable cité plus haut, un CSV synthétique de clients (`demo/clients.csv`, avec nom, courriel, téléphone, date et commentaire, presque toutes les valeurs différentes) se traite à environ 100 ko/s : 160 000 lignes (20 Mo) en 3 min 20, avec 560 Mo de mémoire au plus, modèle compris (ses poids, 287 Mo, sont lus dans le fichier). Il faut donc compter une quinzaine de minutes pour 80 Mo. La mémoire ne dépend pas de la taille du fichier : elle tient au modèle, aux tampons de l'encodeur, proportionnels au texte qu'il lit d'un coup (d'où des parts de 4 ko), et aux plafonds de la liste et du cache. Avec des parts de 64 ko, le traitement gagnait 13 % de vitesse mais montait à 2,3 Go. Dans le navigateur, sur un seul cœur, c'est dix fois plus lent au moins : la version en ligne convient à des fichiers de quelques mégaoctets. Le serveur traite un document à la fois : un gros CSV fait attendre les suivants, et la page l'indique.

## Vidéo

`make video`, le serveur démarré, enregistre une démonstration sous-titrée de 1 min 30 (`video/demo-caviardage.mp4`) à partir du document fictif de `demo/`. Il faut Chromium, LibreOffice, Node et ffmpeg.

## Licence

GPL-3.0, celle de go-anon, avec lequel le binaire est lié.
