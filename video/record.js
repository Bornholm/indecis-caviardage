// Records the demonstration: drives the page with a visible cursor, keeps
// the caption timeline, writes demo.webm and captions.srt.
const { chromium } = require('playwright-core');
const fs = require('fs');
const path = require('path');

const W = 1280, H = 720;
const here = __dirname;
const file = (n) => 'http://127.0.0.1:8072/' + n;
const result = JSON.parse(fs.readFileSync(path.join(here, 'r.json')));
const redetected = (result.redetected || []).map((x) => x.text);

const captions = [];
let t0 = 0, current = null;
function say(text) {
  const now = Date.now() - t0;
  if (current) current.end = now;
  current = text ? { start: now, text } : null;
  if (current) captions.push(current);
}
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// A visible cursor: Playwright videos do not show the mouse.
const cursor = `
  window.addEventListener('DOMContentLoaded', () => {
    const c = document.createElement('div');
    c.style.cssText = 'position:fixed;z-index:99999;left:-50px;top:-50px;width:22px;height:22px;border-radius:50%;background:rgba(255,255,255,.85);border:2px solid #0b1020;box-shadow:0 0 0 4px rgba(106,166,255,.45);pointer-events:none;transform:translate(-50%,-50%);transition:transform .1s';
    document.body.appendChild(c);
    document.addEventListener('mousemove', (e) => { c.style.left = e.clientX + 'px'; c.style.top = e.clientY + 'px'; });
    document.addEventListener('mousedown', () => { c.style.transform = 'translate(-50%,-50%) scale(.7)'; });
    document.addEventListener('mouseup', () => { c.style.transform = 'translate(-50%,-50%)'; });
  });`;

async function moveTo(page, sel, opts = {}) {
  const box = await page.locator(sel).first().boundingBox();
  const x = box.x + (opts.dx ?? box.width / 2), y = box.y + (opts.dy ?? box.height / 2);
  await page.mouse.move(x, y, { steps: 25 });
  return { x, y };
}

(async () => {
  const browser = await chromium.launch({ executablePath: '/usr/bin/chromium' });
  const ctx = await browser.newContext({ viewport: { width: W, height: H }, recordVideo: { dir: path.join(here, 'raw'), size: { width: W, height: H } } });
  await ctx.addInitScript(cursor);
  const page = await ctx.newPage();
  t0 = Date.now();

  await page.goto(file('title.html'));
  say("Objectif : retirer les données personnelles d'un document de travail avant de le partager.");
  await wait(5500);
  say("Pas seulement les noms : tout ce qui permet de reconnaître quelqu'un, et les données sensibles.");
  await wait(5000);

  await page.goto(file('compare.html'));
  await page.evaluate(() => { document.querySelector('.cols').style.gridTemplateColumns = '1fr 0fr'; });
  say("Le document : un compte rendu RH fictif, au format Word.");
  await wait(4000);
  say("Il cite une salariée, ses collègues, son NIR, sa santé, sa vie familiale, sa religion…");
  await page.mouse.move(400, 400, { steps: 10 });
  for (let i = 0; i < 6; i++) { await page.mouse.wheel(0, 120); await wait(500); }
  await wait(2500);

  await page.goto('http://127.0.0.1:8071/');
  await page.mouse.move(640, 360);
  say("Le démonstrateur tourne sur ce portable : rien n'est envoyé ailleurs.");
  await wait(3500);
  say("On dépose le document Word (.docx ou .odt).");
  await moveTo(page, '#drop');
  await wait(600);
  await page.setInputFiles('#file', path.join(here, 'original.docx'));
  await wait(2200);

  say("Le curseur « Prudence » règle le compromis entre fuites et masques en trop.");
  const s = await moveTo(page, '#bias', { dx: 0 });
  await wait(700);
  await page.mouse.down();
  const box = await page.locator('#bias').boundingBox();
  for (const v of [6, 7, 5]) {
    await page.mouse.move(box.x + (v - 3) / 5 * box.width, s.y, { steps: 12 });
    await page.evaluate((v) => { const b = document.getElementById('bias'); b.value = v; b.dispatchEvent(new Event('input')); }, v);
    await wait(1300);
  }
  await page.mouse.up();
  say("À 5, environ 2 % des passages personnels restent lisibles dans nos tests.");
  await wait(3500);

  say("Caviarder : le modèle lit tout le document, go-anon réécrit le .docx et vérifie le résultat.");
  await moveTo(page, '#go');
  await wait(500);
  await page.click('#go');
  await page.waitForSelector('ul.items', { timeout: 60000 });
  await wait(2500);
  const secs = await page.evaluate(() => document.querySelectorAll('.summary .big')[2].textContent);
  say(`Résultat en ${secs} : ${result.elements.length} éléments distincts masqués, et le document à télécharger.`);
  await wait(4500);
  say("La vérification relit le document produit et liste ce qui est resté en clair, à contrôler.");
  await moveTo(page, 'ul.redetected');
  await wait(4000);
  say("Ici, « vendredi » renvoie à la pratique religieuse de la salariée : un oubli à masquer à la main.");
  await wait(4500);

  say("Chaque élément est listé avec son type et son contexte : personnes, identifiants, dates, lieux…");
  await page.mouse.move(800, 500, { steps: 15 });
  for (let i = 0; i < 5; i++) { await page.mouse.wheel(0, 140); await wait(600); }
  await wait(1500);
  say("… mais aussi les quasi-identifiants : « séparée de son mari », « seule infirmière formée à… ».");
  for (let i = 0; i < 4; i++) { await page.mouse.wheel(0, 140); await wait(600); }
  await wait(2500);

  await page.mouse.wheel(0, -3000);
  await wait(800);
  say("Les données sensibles (article 9 du RGPD) sont signalées : ici, santé et syndicat.");
  const chip = page.locator('.chip.sens').first();
  if (await chip.count()) {
    await moveTo(page, '.chip.sens');
    await wait(400);
    await chip.click();
  }
  await wait(3000);
  say("Un clic sur un passage masqué le révèle, pour vérifier.");
  await moveTo(page, 'ul.items mark');
  await wait(500);
  await page.locator('ul.items mark').first().click();
  await wait(3500);

  await page.goto(file('compare.html'));
  say("Le document produit garde sa mise en forme ; métadonnées, commentaires et révisions sont purgés.");
  await page.mouse.move(960, 400, { steps: 10 });
  await wait(3000);
  for (let i = 0; i < 6; i++) {
    await page.evaluate((y) => { for (const f of document.querySelectorAll('iframe')) f.contentWindow.scrollTo(0, y); }, 130 * (i + 1));
    await wait(700);
  }
  say("Un contrôle humain reste nécessaire : le modèle masque large plutôt que de laisser fuir.");
  await wait(4500);

  await page.goto(file('end.html'));
  say("Performances mesurées sur un portable, sans GPU.");
  await wait(7500);
  say(null);
  await wait(300);

  const video = page.video();
  await ctx.close();
  await browser.close();
  fs.renameSync(await video.path(), path.join(here, 'demo.webm'));
  const ts = (ms) => {
    const h = Math.floor(ms / 3600000), m = Math.floor(ms / 60000) % 60, s = Math.floor(ms / 1000) % 60, r = ms % 1000;
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')},${String(r).padStart(3, '0')}`;
  };
  fs.writeFileSync(path.join(here, 'captions.srt'), captions.map((c, i) => `${i + 1}\n${ts(c.start)} --> ${ts(c.end)}\n${c.text}\n`).join('\n'));
  console.log('duration', (Date.now() - t0) / 1000, 's,', captions.length, 'captions');
})();
