# indecis-caviardage

Un démonstrateur de caviardage RGPD : on dépose un document Word ou OpenDocument, il en masque les données personnelles et liste ce qu'il a masqué. Tout tourne sur la machine, sur le processeur, en Go pur : le document n'est envoyé nulle part et n'est pas conservé.

## Ce que le serveur garde, et combien de temps

- Le document reçu et le document produit sont écrits pendant le traitement dans un dossier de `/dev/shm`, en mémoire, jamais sur disque quand ce répertoire existe ; le dossier est supprimé à la fin de la requête, et au démarrage si un arrêt brutal l'avait laissé.
- Le document caviardé attend son téléchargement en mémoire, au plus 15 minutes. Il est supprimé dès le premier téléchargement, ou à son expiration (purge chaque minute), ses octets effacés.
- La liste des éléments masqués, qui contient les valeurs d'origine, est envoyée au navigateur pour l'affichage ; le serveur ne la garde pas.
- Les journaux ne portent que le format, la taille, le nombre d'éléments et la durée, jamais de contenu.
- Go ne remet pas à zéro la mémoire qu'il libère : des fragments peuvent subsister dans le processus tant qu'elle n'est pas réutilisée. Arrêter le serveur après usage les efface.

```bash
make run    # http://127.0.0.1:8071
```

1. Déposer un fichier `.docx` ou `.odt`.
2. Régler la **prudence** : plus elle est haute, moins il reste de données lisibles, et plus il y a de masques superflus. À 5, environ 2 % des passages personnels restaient exposés dans nos tests en prose ; à 6, environ 1 %.
3. Choisir entre le **caviardage** (██████, irréversible) et la **pseudonymisation** (⟦PERSON_1⟧ : une même personne garde le même pseudonyme dans tout le document).
4. Télécharger le document produit, et parcourir la liste des éléments masqués : type, contexte, nombre d'occurrences, et catégorie sensible (santé, syndicat, convictions, origine, opinions politiques, vie sexuelle, infractions).

## Fonctionnement

Un modèle [indecis](https://github.com/Bornholm/indecis) de 7,7 millions de paramètres, entraîné dans [indecis-ner](../indecis-ner), lit le document en entier et repère les passages à masquer : identifiants directs (nom, NIR, courriel, téléphone, adresse), quasi-identifiants (métier, lieu, date, proches, événements de vie) et données sensibles de l'article 9. [go-anon](../go-anon) réécrit le document dans son format, y repasse une détection pour vérifier, et en retire les métadonnées, commentaires et révisions ; ses expressions régulières complètent le modèle pour les identifiants (courriel, IBAN, téléphone, SIRET).

La liste vient de la table de correspondance de go-anon : elle donne exactement ce qui a été remplacé. Le traitement pseudonymise d'abord, puis, en mode caviardage, remplace les pseudonymes par des blocs.

Sur un portable (Core Ultra 7 265U, modèle int8), dix pages A4 se traitent en 1,3 s sur un cœur et environ 320 Mo de mémoire. Les mesures de qualité et leurs limites sont dans le README d'indecis-ner.

## Limites

- Le modèle masque large plutôt que de laisser fuir : des titres ou des mots courants sont parfois masqués. Un contrôle humain reste nécessaire avant toute diffusion.
- La catégorie sensible d'un passage est indicative : le masquage est fiable, la catégorisation beaucoup moins.
- go-anon réunit les lignes d'un même paragraphe séparées par des sauts de ligne simples : « Chalon-sur-Saône » et « Date » à la ligne suivante deviennent « Chalon-sur-SaôneDate ». Les paragraphes distincts ne sont pas concernés.
- Pas de PDF dans ce démonstrateur, bien que go-anon les traite.

## Vidéo

`make video`, le serveur démarré, enregistre une démonstration sous-titrée de 1 min 30 (`video/demo-caviardage-rgpd.mp4`) à partir du document fictif de `demo/`. Il faut Chromium, LibreOffice, Node et ffmpeg.

## Licence

GPL-3.0, celle de go-anon, avec lequel le binaire est lié.
