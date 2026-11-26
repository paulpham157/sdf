const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, 'output');

const PAGES = [
  { name: 'home', path: '/', image: true },
  { name: 'ledger', path: '/ledger/', table: true, code: true },
  { name: 'action', path: '/action/', code: true },
  { name: 'security', path: '/security/', admonition: true },
];

const LIGHT = {
  name: 'light',
  attribute: 'default',
  background: 'rgb(246, 241, 232)',
  code: 'rgb(235, 227, 214)',
  inlineCode: 'rgb(235, 227, 214)',
  palette: {
    index: 1,
    color: {
      media: '(prefers-color-scheme: light)',
      scheme: 'default',
      primary: 'custom',
      accent: 'custom',
    },
  },
};

const DARK = {
  name: 'dark',
  attribute: 'slate',
  background: 'rgb(27, 31, 37)',
  code: 'rgb(15, 18, 22)',
  inlineCode: 'rgb(36, 42, 50)',
  palette: {
    index: 2,
    color: {
      media: '(prefers-color-scheme: dark)',
      scheme: 'slate',
      primary: 'custom',
      accent: 'custom',
    },
  },
};

function installScheme(context, palette) {
  return context.addInitScript((stored) => {
    localStorage.setItem('/.__palette', JSON.stringify(stored));
    window.__schemeAtPaint = null;
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          if (window.__schemeAtPaint == null && document.body) {
            window.__schemeAtPaint = document.body.getAttribute('data-md-color-scheme');
            window.__paintName = entry.name;
          }
        }
      }).observe({ type: 'paint', buffered: true });
    } catch (err) {
      window.__schemeAtPaint = 'observer-failed';
    }
  }, palette);
}

async function open(page, pathname) {
  await page.goto(pathname, { waitUntil: 'networkidle' });
  await page.waitForFunction(() => window.__schemeAtPaint != null);
}

async function contrast(page) {
  const results = await new AxeBuilder({ page })
    .options({ runOnly: { type: 'rule', values: ['color-contrast'] } })
    .analyze();
  expect(results.violations).toEqual([]);
}

async function shot(page, name) {
  fs.mkdirSync(OUT, { recursive: true });
  await page.screenshot({ path: path.join(OUT, name), fullPage: false });
}

for (const scheme of [LIGHT, DARK]) {
  test.describe(scheme.name, () => {
    test.beforeEach(async ({ context }) => {
      await context.addInitScript(() => localStorage.clear());
      await installScheme(context, scheme.palette);
    });

    for (const item of PAGES) {
      test(item.name + ' has no color-contrast violations', async ({ page }) => {
        await page.setViewportSize({ width: 1440, height: 900 });
        await open(page, item.path);
        expect(await page.evaluate(() => window.__schemeAtPaint)).toBe(scheme.attribute);
        await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', scheme.attribute);
        await expect(page.locator('body')).toHaveCSS('background-color', scheme.background);
        if (item.code) {
          await expect(page.locator('.md-typeset pre').first()).toHaveCSS('background-color', scheme.code);
          await expect(page.locator('.md-typeset p code, .md-typeset li code').first()).toHaveCSS(
            'background-color',
            scheme.inlineCode,
          );
        }
        if (item.table) {
          await expect(page.locator('.md-typeset table').first()).toBeVisible();
        }
        if (item.admonition) {
          await expect(page.locator('.admonition.warning')).toBeVisible();
        }
        if (item.image && scheme.name === 'dark') {
          const shot = page.locator('.md-typeset img[src*="images/"]');
          const diagram = page.locator('.md-typeset img[src*="diagrams/"]');
          await expect(shot).toHaveCSS('padding-top', '0px');
          await expect(diagram).toHaveCSS('padding-top', '12px');
          await expect(diagram).toHaveCSS('background-color', 'rgb(246, 241, 232)');
        }
        if (item.image && scheme.name === 'light') {
          await expect(page.locator('.md-typeset img').first()).toHaveCSS('padding-top', '0px');
        }
        const active = page.locator('.md-sidebar--primary a.md-nav__link--active').first();
        await expect(active).toBeVisible();
        await expect(active).toHaveAttribute('aria-current', 'page');
        if (scheme.name === 'dark') {
          await expect(active).toHaveCSS('background-color', 'rgb(237, 230, 218)');
          await expect(active).toHaveCSS('color', 'rgb(27, 31, 37)');
          await expect(active).toHaveCSS('box-shadow', 'none');
          await expect(active).toHaveCSS('padding-left', '8px');
          await expect(active).toHaveCSS('padding-right', '8px');
        }
        await contrast(page);
        await shot(page, scheme.name + '-' + item.name + '-1440.png');
      });
    }
  });
}

test('system preference is applied before first paint', async ({ browser }) => {
  for (const [media, attribute] of [['dark', 'slate'], ['light', 'default']]) {
    const context = await browser.newContext({ colorScheme: media });
    await installScheme(context, {
      index: 0,
      color: {
        media: '(prefers-color-scheme)',
        scheme: 'default',
        primary: 'custom',
        accent: 'custom',
      },
    });
    const page = await context.newPage();
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page, '/');
    expect(await page.evaluate(() => window.__schemeAtPaint)).toBe(attribute);
    await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', attribute);
    await context.close();
  }
});

test('dark drawer at 390', async ({ context, page }) => {
  await installScheme(context, DARK.palette);
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page, '/');
  expect(await page.evaluate(() => window.__schemeAtPaint)).toBe('slate');
  await page.locator('.md-header label[for="__drawer"]').click();
  await expect(page.locator('.md-sidebar--primary')).toBeVisible();
  await contrast(page);
  await shot(page, 'dark-home-390-drawer.png');
});

test('dark OS with no stored palette is slate before first paint', async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: 'dark' });
  await context.addInitScript(() => {
    localStorage.clear();
    window.__schemeAtPaint = null;
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          if (window.__schemeAtPaint == null && document.body) {
            window.__schemeAtPaint = document.body.getAttribute('data-md-color-scheme');
          }
        }
      }).observe({ type: 'paint', buffered: true });
    } catch (err) {
      window.__schemeAtPaint = 'observer-failed';
    }
  });
  const page = await context.newPage();
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, '/');
  expect(await page.evaluate(() => window.__schemeAtPaint)).toBe('slate');
  await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', 'slate');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
  await context.close();
});

test('system mode follows an OS theme change', async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: 'dark' });
  await installScheme(context, {
    index: 0,
    color: {
      media: '(prefers-color-scheme)',
      scheme: 'default',
      primary: 'custom',
      accent: 'custom',
    },
  });
  const page = await context.newPage();
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, '/');
  await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', 'slate');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');

  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', 'default');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'light');

  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('body')).toHaveAttribute('data-md-color-scheme', 'slate');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
  await context.close();
});

test('explicit light on a dark OS keeps a light canvas', async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: 'dark' });
  await installScheme(context, LIGHT.palette);
  const page = await context.newPage();
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, '/');
  expect(await page.evaluate(() => window.__schemeAtPaint)).toBe('default');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'light');
  await context.close();
});

test('diagrams page in both schemes', async ({ browser }) => {
  for (const scheme of [LIGHT, DARK]) {
    const context = await browser.newContext();
    await installScheme(context, scheme.palette);
    const page = await context.newPage();
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page, '/diagrams/');
    expect(await page.evaluate(() => window.__schemeAtPaint)).toBe(scheme.attribute);
    const figure = page.locator('.md-typeset a:has(> img.sdf-diagram)').first();
    await figure.scrollIntoViewIfNeeded();
    await expect(figure).toHaveCSS('border-bottom-style', 'none');
    const img = figure.locator('img');
    await expect.poll(async () => {
      const size = await img.boundingBox();
      return size ? size.height : 0;
    }).toBeGreaterThan(80);
    if (scheme.name === 'dark') {
      await expect(img).toHaveCSS('background-color', 'rgb(246, 241, 232)');
      await expect(img).toHaveCSS('padding-top', '12px');
    }
    await contrast(page);
    await shot(page, scheme.name + '-diagrams-1440.png');
    await context.close();
  }
});
