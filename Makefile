# Caviardage RGPD de documents Word et ODT, avec indecis et go-anon.
#
#   make run     compile et démarre sur http://127.0.0.1:8071
#   make video   enregistre la vidéo de démonstration (serveur démarré)

GOEXPERIMENT ?= simd
export GOEXPERIMENT

MODEL ?= ../indecis-ner/runs/work-fam

.PHONY: build run video clean

build:
	CGO_ENABLED=0 go build -o bin/indecis-caviardage .

run: build
	bin/indecis-caviardage -model $(MODEL)

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
		-c:v libx264 -preset slow -crf 22 -pix_fmt yuv420p -movflags +faststart demo-caviardage-rgpd.mp4

clean:
	rm -rf bin
