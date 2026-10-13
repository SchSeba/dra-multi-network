// Run with Playwright available in NODE_PATH; no project dependencies required.
const assert = require('node:assert/strict');
const { pathToFileURL } = require('node:url');
const path = require('node:path');
const { chromium } = require('playwright');
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(pathToFileURL(path.join(__dirname, 'index.html')).href);
    assert.equal(await page.locator('.slide').count(), 26);
    assert.equal(await page.locator('.slide.active h1').evaluate(el => getComputedStyle(el).color), 'rgb(239, 244, 235)');
    for (let i = 1; i <= 26; i++) {
      await page.evaluate(n => go(n - 1), i);
      assert.equal(await page.locator('.slide.active').count(), 1);
      assert.equal(await page.locator('#counter').textContent(), `${i} / 26`);
      assert.equal(await page.locator('.slide.active h1').evaluate(el => getComputedStyle(el).color), 'rgb(239, 244, 235)');
      assert.equal(await page.locator('.slide.active').evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(16, 39, 51)');
      assert.equal(await page.locator('.slide.active footer').textContent(), `PROJECT WALKTHROUGH${String(i).padStart(2, '0')} / 26`);
      const overflow = await page.locator('.slide.active').evaluate(slide => {
        const bounds = slide.getBoundingClientRect();
        return [...slide.querySelectorAll('h1,h3,p,pre,.yaml-line,svg text,article,footer,th,td,.profile-ownership,.profile-note,.quota-boundary,.quota-placeholder')].filter(el => {
          const r = el.getBoundingClientRect();
          return r.right > bounds.right + 1 || r.bottom > bounds.bottom + 1 || r.left < bounds.left - 1;
        }).map(el => el.textContent);
      });
      assert.deepEqual(overflow, [], `Slide ${i} overflow`);
      const footerOverlap = await page.locator('.slide.active').evaluate(slide => {
        const note = slide.querySelector('.profile-note');
        return note && note.getBoundingClientRect().bottom > slide.querySelector('footer').getBoundingClientRect().top - 6;
      });
      assert.ok(!footerOverlap, `Slide ${i} overlaps footer`);
    }
    await page.keyboard.press('Home');
    await page.keyboard.press('ArrowRight');
    assert.equal(await page.locator('#counter').textContent(), '2 / 26');
    await page.keyboard.press('n');
    assert.equal(await page.locator('#notes').getAttribute('class'), 'open');
    assert.match(await page.locator('#note-body').textContent(), /primary CNI/);
    await page.keyboard.press('Escape');
    await page.keyboard.press('o');
    await page.locator('#overview button').nth(18).click();
    assert.equal(await page.locator('#counter').textContent(), '19 / 26');
    await page.locator('#overview-toggle').blur();
    await page.keyboard.press('End');
    assert.equal(await page.locator('#counter').textContent(), '26 / 26');
    await page.reload();
    assert.equal(await page.locator('#counter').textContent(), '26 / 26');
    await page.setViewportSize({ width: 800, height: 600 });
    await page.waitForFunction(() => document.querySelector('.slide.active').getBoundingClientRect().width <= 800);
    const b = await page.locator('.slide.active').boundingBox();
    assert.ok(b.width <= 800 && b.height <= 600);
    assert.deepEqual(errors, []);
    if (process.argv[2]) {
      await page.setViewportSize({ width: 1440, height: 900 });
      for (const n of [1, 6, 7, 8, 14, 15, 19, 21, 22, 23, 24, 26]) {
        await page.evaluate(n => go(n - 1), n);
        await page.screenshot({ path: path.join(process.argv[2], `slide-${n}.png`) });
      }
      await page.pdf({ path: path.join(process.argv[2], 'dra-multi-network.pdf'), printBackground: true, preferCSSPageSize: true });
    }
    console.log('PASS: all 26 slides fit; navigation, notes, overview, URL restore and resizing work; no JS errors.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
