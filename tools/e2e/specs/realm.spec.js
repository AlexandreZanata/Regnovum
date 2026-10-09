// P60-T01: functional dashboard over the real feed/search/participation server.
import { expect, test } from "@playwright/test";

test("home opens directly into real arenas, with optimized images and all role portraits", async ({ page }) => {
  const broken = [];
  page.on("response", response => {
    if (response.url().includes("/assets/") && response.status() >= 400) broken.push(response.url());
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Painel do Reino", exact: true })).toBeVisible();
  const cards = page.locator(".ga-arena-card");
  expect(await cards.count()).toBeGreaterThan(0);
  await expect(cards.first().getByRole("link", { name: "Entrar na arena" })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Buscar arenas" })).toBeVisible();
  await page.getByText(/^Todos os cargos/).click();
  expect(await page.locator(".ga-role-card").count()).toBe(17);
  await page.locator("#roles").scrollIntoViewIfNeeded();
  for (const portrait of await page.locator(".ga-role-card img").all()) {
    await portrait.scrollIntoViewIfNeeded();
    await expect.poll(() => portrait.evaluate(image => image.complete && image.naturalWidth > 0)).toBe(true);
  }
  expect(broken).toEqual([]);
  await page.locator(".ga-role-card summary").first().click();
  await expect(page.getByText("O retrato não concede permissões.", { exact: true }).first()).toBeVisible();
});

test("search, empty state, category filter and participation use native URLs", async ({ page }) => {
  await page.goto("/");
  const title = await page.locator(".ga-arena-card h3").first().innerText();
  const arena = await page.locator(".ga-arena-card h3 a").first().getAttribute("href");
  await page.getByRole("searchbox").fill(title.slice(0, 100));
  await page.getByRole("button", { name: "Buscar", exact: true }).click();
  await expect(page).toHaveURL(/\?q=/);
  await expect(page.locator(".ga-arena-card h3").filter({ hasText: title })).toHaveCount(1);
  await page.getByRole("searchbox").fill("zzzzzzzzzzzzzzzzzzzzzzzz");
  await page.getByRole("button", { name: "Buscar", exact: true }).click();
  await expect(page.getByText("Nenhuma arena encontrada.", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Tecnologia", exact: true }).click();
  await expect(page).toHaveURL(/category=technology/);
  expect(await page.locator(".ga-arena-card").count()).toBeGreaterThan(0);
  await page.goto(arena);
  await expect(page.locator("main")).toContainText(title);
});

test("small screens, keyboard menu and skip link stay usable", async ({ page }) => {
  for (const width of [360, 640, 1024, 1672]) {
    await page.setViewportSize({ width, height: 960 });
    await page.goto("/");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  }
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Ir para as arenas" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("main")).toBeFocused();
  const menu = page.getByRole("button", { name: "Menu" });
  await menu.click();
  await expect(menu).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Escape");
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await expect(menu).toBeFocused();
});

test("the dashboard remains functional without JavaScript and refuses forged cursors", async ({ browser, baseURL }) => {
  const context = await browser.newContext({ javaScriptEnabled: false, locale: "pt-BR", baseURL });
  try {
    const page = await context.newPage();
    await page.goto("/");
    expect(await page.locator(".ga-arena-card").count()).toBeGreaterThan(0);
    await expect(page.getByRole("navigation", { name: "Navegação principal" })).toBeVisible();
    await page.getByRole("link", { name: "Tecnologia", exact: true }).click();
    await expect(page).toHaveURL(/category=technology/);
    const refusal = await page.goto("/?cursor=forged");
    expect(refusal.status()).toBe(400);
    await expect(page.getByRole("alert")).toContainText("Busca ou paginação inválida");
    expect(await page.locator(".ga-arena-card").count()).toBe(0);
  } finally {
    await context.close();
  }
});
