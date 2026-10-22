# indecis-caviardage

Un démonstrateur de caviardage de données personnelles : on dépose un document Word ou OpenDocument, ou un fichier CSV, il en masque les données personnelles et liste ce qu'il a masqué. Tout tourne sur la machine, sur le processeur, en Go pur : le document n'est envoyé nulle part et n'est pas conservé.

La version tout navigateur est en ligne : https://bornholm.github.io/indecis-caviardage/

## Le modèle

Le modèle `work-fam-p576` d'[indecis-ner](https://github.com/Bornholm/indecis-ner) est joint à ses releases depuis la v0.2.0 : c'est `work-fam` réduit à la moitié de ses neurones MLP puis distillé, 15 % plus rapide pour des masques équivalents. Les commandes `make` l'attendent dans `../indecis-ner/runs/work-fam-p576`, ou dans le dossier donné par `MODEL` :

```bash
gh release download v0.2.0 --repo Bornholm/indecis-ner --dir model
make run MODEL=model
```

## Ce que le serveur garde, et combien de temps

- Le document reçu attend son traitement, et le document produit son téléchargement, en **mémoire secrète** (`memfd_secret`, Linux 5.14 et plus) : des pages retirées de la projection du noyau, qu'aucun autre processus ne peut lire, root compris (ni `/proc/<pid>/mem`, ni ptrace), qui ne vont jamais en swap et restent hors des core dumps. Le formulaire est lu au fil de l'envoi et le document copié directement en mémoire secrète : il ne passe pas par le répertoire temporaire du système, où le traitement standard des formulaires en Go l'écrirait au-delà de 32 Mo.
- Un CSV est lu et écrit en mémoire secrète. Les lecteurs Word et ODT de go-anon ne lisant que des fichiers, un document Word ou ODT est copié pendant son traitement seulement dans un dossier de `/dev/shm` (en mémoire, jamais sur disque quand ce répertoire existe) ; ses fichiers sont ensuite remplis de zéros puis supprimés, et le dossier l'est aussi au démarrage si un arrêt brutal l'avait laissé.
- Le document produit attend son téléchargement au plus 15 minutes. Il est effacé dès le premier téléchargement, ou à son expiration (purge chaque minute) : sa mémoire est remplie de zéros, puis rendue.
- Pendant son traitement, le texte d'un document passe aussi dans la mémoire de Go, qui ne peut pas être secrète. Le processus se déclare donc non inspectable (`PR_SET_DUMPABLE=0`) : pas de core dump, et ni ptrace ni `/proc/<pid>/mem` pour les autres processus du même utilisateur ; root, lui, y garde accès.
- Ce que la mémoire secrète ne couvre pas : le processus lui-même s'il est compromis, et l'hyperviseur d'une machine virtuelle, qui lit la mémoire de l'invité.
- La liste des éléments masqués, qui contient les valeurs d'origine, est envoyée au navigateur pour l'affichage ; le serveur ne la garde pas.
- Les journaux ne portent que le format, la taille, le nombre d'éléments et la durée, jamais de contenu.
- Go ne remet pas à zéro la mémoire qu'il libère : des fragments peuvent subsister dans le processus tant qu'elle n'est pas réutilisée. Arrêter le serveur après usage les efface.

```bash
make run    # http://127.0.0.1:8071
```

Le serveur refuse de démarrer sans mémoire secrète. Elle se décompte de la limite de mémoire verrouillée (`RLIMIT_MEMLOCK`), souvent de 8 Mo seulement : à relever en production, à hauteur des documents en attente et en cours, chacun avec son résultat (un CSV produit fait environ 1,5 fois l'original). Par exemple, pour `-max-size` de 200 Mo, 2 instances et une file de 8 : `LimitMEMLOCK=5G` dans l'unité systemd, ou `--ulimit memlock=5368709120` pour un conteneur. Le serveur avertit au démarrage si la limite ne tient pas un document de `-max-size`, et répond 503 à un document qui la dépasserait.

1. Déposer un fichier `.docx`, `.odt`, `.csv` ou `.tsv`.
2. Régler la **prudence** : plus elle est haute, moins il reste de données lisibles, et plus il y a de masques superflus. À 5, environ 2 % des passages personnels restaient exposés dans nos tests en prose ; à 6, environ 1 %.
3. Choisir entre le **caviardage** (██████, irréversible) et la **pseudonymisation** (⟦PERSON_1⟧ : une même personne garde le même pseudonyme dans tout le document).
4. Au besoin, donner des **termes à garder lisibles** (noms d'outils, de services, de produits) ou des types de passages à ne plus masquer : voir plus bas.
5. Télécharger le document produit, et parcourir la liste des éléments masqués : type, contexte, nombre d'occurrences, et catégorie sensible (santé, syndicat, convictions, origine, opinions politiques, vie sexuelle, infractions). Chaque élément peut y être libéré, puis le document régénéré.

## Garder des passages lisibles

Le modèle masque large : dans des verbatims d'entretiens, il masque aussi les services de l'entreprise ou ses outils. Quatre moyens de les laisser lisibles, appliqués par le serveur comme par la version navigateur :

- **Des termes à garder** : chacun est retiré des masques partout où il apparaît, quelle que soit la casse, en mot entier (« Jira » dans « Jira, » mais pas dans « Jiraffe »). Ce qui reste d'un masque autour d'un terme reste masqué. Les masques d'une catégorie sensible ne sont pas découpés : ils ne sont libérés que si un terme les couvre en entier.
- **Des types** : lieux, organisations, caractéristiques, dates, montants, autres. Les données sensibles restent masquées, et les noms, identifiants, courriels et téléphones ne se libèrent qu'un par un. À manier avec prudence : le modèle réunit des passages voisins en un seul masque, d'un seul type, et une catégorie sensible ne couvre pas toujours tout ce qui la révèle. Dans le document de `demo/`, « ses enfants à la synagogue de Chalon » sort en lieu, mais avec la catégorie convictions : il reste masqué quand on libère les lieux. « l'office qu'elle fréquente avec », juste avant, sort en « autre », sans catégorie : libérer ce type le rend lisible.
- **La liste** : un clic sur « Libérer », puis « Régénérer le document », qui relance le traitement avec ces passages pour termes. Le navigateur renvoie le document, qu'il a encore : le serveur ne garde pas l'original. La page propose alors d'étendre le choix, sans rien appliquer d'elle-même : aux autres masques de confiance inférieure ou égale à celle des passages libérés, ou, quand la plupart des passages d'un type ont été libérés, au type entier. Ces suggestions laissent de côté les noms, identifiants et données sensibles.
- **La retaille** : un clic sur « Retailler », puis sur les mots d'un masque à laisser lisibles ; les autres restent masqués, avec le type, la catégorie sensible et la confiance du masque. « Son fils aîné est suivi au CMP de Chalon pour des troubles de l'attention » peut ne garder masqué que « CMP de Chalon » et « troubles de l'attention ». La page envoie, pour chaque masque retaillé, son texte et les parties qui restent masquées ; le serveur ne retaille que les masques au texte identique, partout où ils apparaissent, et garde le masque entier s'il n'y retrouve pas une partie. Les mots laissés lisibles ne sont pas signalés par la vérification de go-anon autour du masque retaillé, sauf un identifiant, qu'elle signale quoi qu'on ait choisi.

La confiance est celle du modèle pour chaque masque. Dans le document de `demo/`, sous 0,35, on trouve surtout des masques superflus (« 6 mars », « son médecin traitant », « 80 % », « CPAM ») ; mais « Son fils aîné est suivi au CMP de Chalon » est à 0,43. La page montre donc les masques concernés avant de les libérer.

Les réglages (prudence, mode, termes, types) sont gardés dans le `localStorage` du navigateur. Les passages libérés ou retaillés depuis la liste ne le sont pas, puisqu'il peut s'agir de noms, sauf à ajouter les passages libérés aux termes à garder.

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

Sur un portable (Core Ultra 7 265U, modèle int8), dix pages A4 de comptes rendus riches en données personnelles (30 000 caractères, 370 passages remplacés) se traitent en 1,6 s sur un cœur et environ 400 Mo de mémoire. Les mesures de qualité et leurs limites sont dans le README d'indecis-ner.

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

Le débit dépend du modèle, qui lit chaque cellule. Sur le portable cité plus haut, un CSV synthétique de clients (`demo/clients.csv`, avec nom, courriel, téléphone, date et commentaire, presque toutes les valeurs différentes) se traite à environ 290 ko/s : 160 000 lignes (20 Mo) en 1 min 15, avec 570 Mo de mémoire au plus, modèle compris (ses poids, 276 Mo, sont lus dans le fichier). Le même fichier répété jusqu'à 80 Mo (640 000 lignes) prend 4 min 55, à 284 ko/s, avec indecis v0.7.0 (5 min 09 avec la v0.6.0). La mémoire ne dépend pas de la taille du fichier : elle tient au modèle, aux tampons de l'encodeur, proportionnels au texte qu'il lit d'un coup (d'où des parts de 4 ko), et aux plafonds de la liste et du cache. Avec des parts de 64 ko, le traitement gagnait 13 % de vitesse mais montait à 2,3 Go. Dans le navigateur, sur un seul cœur, c'est dix fois plus lent au moins : la version en ligne convient à des fichiers de quelques mégaoctets.

## Montée en charge

Le serveur traite plusieurs documents à la fois (`-workers`, 2 par défaut), chacun avec sa propre instance du modèle, puisque la prudence se règle sur le modèle. Les instances partagent ce qui peut l'être : la table d'embeddings, l'essentiel des 276 Mo du modèle, est lue dans le fichier et ses pages sont communes ; le tokenizer est chargé une fois (`LoadShared` d'indecis). Une instance de plus coûte 2 à 3 Mo au repos.

Le calcul aussi est partagé : le modèle lit au plus quatre textes à la fois, tous documents confondus. Quatre parts d'un lot CSV occupent déjà tous les cœurs, et chaque lecture tient les tampons de l'encodeur (environ 60 Mo). Les documents en cours se relaient au lieu de s'additionner. Mesuré sur le portable cité plus haut, quatre CSV de 1 Mo envoyés en même temps (limite de mémoire verrouillée relevée à 1 Go : avec 8 Mo, une partie recevait un 503) :

| Instances | Durée totale | Durée par document | Mémoire privée max |
| --- | --- | --- | --- |
| 1 | 22 s | 6, 11, 17, 22 s | 400 Mo |
| 2 | 17 s | 9, 9, 17, 17 s | 500 Mo |
| 4 | 17 s | 17 s chacun | 550 Mo |

Sans ce partage du calcul, quatre instances montaient à 1,1 Go (mesuré avec indecis v0.4.0). Le débit total dépend peu du nombre d'instances : un document occupe déjà presque tout le processeur. Plusieurs instances servent la réactivité : un petit document n'attend plus la fin d'un gros CSV, ses lectures s'intercalent avec les siennes. Au-delà de `-workers` documents en cours, les suivants attendent, au plus `-queue` (8 par défaut) ; ensuite, le serveur répond 503 avant de recevoir le document, qui prendrait de la mémoire dans le répertoire de traitement.

## Vidéo

`make video`, le serveur démarré, enregistre une démonstration sous-titrée de 1 min 30 (`video/demo-caviardage.mp4`) à partir du document fictif de `demo/`. Il faut Chromium, LibreOffice, Node et ffmpeg.

## Licence

AGPL-3.0. Le binaire est lié à go-anon, sous GPL-3.0, que l'AGPL-3.0 permet de combiner (section 13).
