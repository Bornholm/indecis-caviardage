# Caviardage de données personnelles dans des documents Word et ODT, avec indecis et go-anon.
#
#   make run     compile et démarre sur http://127.0.0.1:8071
#   make wasm    produit dans dist/ la version tout navigateur, en fichiers statiques
#   make serve   produit dist/ et le sert sur http://127.0.0.1:8073
#   make video   enregistre la vidéo de démonstration (serveur démarré)

GOEXPERIMENT ?= simd
export GOEXPERIMENT

MODEL ?= ../indecis-ner/runs/work-fam
MODEL_FILES = config.json indecis.json tokenizer.json model.safetensors
DIST ?= dist

.PHONY: build run wasm serve video clean

build:
	CGO_ENABLED=0 go build -o bin/indecis-caviardage .

run: build
	bin/indecis-caviardage -model $(MODEL)

# Deux modules : avec WebAssembly SIMD, et sans pour les navigateurs qui ne
# le prennent pas en charge (le worker choisit). Le manifeste donne la
# taille des fichiers du modèle, pour la progression, et une version, pour
# le cache du navigateur.
wasm:
	rm -rf $(DIST) && mkdir -p $(DIST)/model
	GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -o $(DIST)/caviardage.wasm .
	GOEXPERIMENT= GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -o $(DIST)/caviardage-nosimd.wasm .
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" wasm/worker.js wasm/memfs.js $(DIST)/
	mkdir -p $(DIST)/demo && cp demo/compte-rendu.docx demo/compte-rendu.odt $(DIST)/demo/
	sed 's/<meta name="mode" content="server">/<meta name="mode" content="local">/' web/index.html > $(DIST)/index.html
	cd $(MODEL) && cp $(MODEL_FILES) $(CURDIR)/$(DIST)/model/
	cd $(DIST)/model && python3 -c 'import hashlib, json, os, sys; f = sys.argv[1:]; h = hashlib.sha256(); [h.update(open(n, "rb").read()) for n in f]; print(json.dumps({"version": h.hexdigest()[:16], "files": [{"name": n, "size": os.path.getsize(n)} for n in f]}))' $(MODEL_FILES) > manifest.json

serve: wasm
	cd $(DIST) && python3 -m http.server 8073 --bind 127.0.0.1

# Rend les documents de la démo, pilote Chromium avec Playwright et incruste
# les sous-titres avec ffmpeg. Demande Chromium, LibreOffice, Node et ffmpeg.
video:
	cd video && npm install --silent && \
	curl -s -F document=@../demo/compte-rendu.docx -F bias=5 -F strategy=redact http://127.0.0.1:8071/api/redact > r.json && \
	curl -s -o caviarde.docx http://127.0.0.1:8071$$(python3 -c "import json;print(json.load(open('r.json'))['download'])") && \
	cp ../demo/compte-rendu.docx original.docx && \
	soffice --headless --convert-to html original.docx >/dev/null && soffice --headless --convert-to html caviarde.docx >/dev/null && \
	(python3 -m http.server 8072 --bind 127.0.0.1 >/dev/null 2>&1 & echo $$! > .http.pid) && sleep 1 && \
	rm -rf raw demo.webm && node record.js; kill $$(cat .http.pid); rm -f .http.pid
	cd video && ffmpeg -y -loglevel error -i demo.webm -vf "subtitles=captions.srt:force_style='FontName=DejaVu Sans,FontSize=15,PrimaryColour=&H00FFFFFF,OutlineColour=&H90000000,BackColour=&H90000000,BorderStyle=3,Outline=6,Shadow=0,MarginV=18,Alignment=2'" \
		-c:v libx264 -preset slow -crf 22 -pix_fmt yuv420p -movflags +faststart demo-caviardage.mp4

clean:
	rm -rf bin $(DIST)
