import fs from "node:fs";

const prototypeRoot = "/tmp/flatline-prototype-src.ko1X6F/_ds/design-system-58676a86-c445-4b18-a440-0237c963dbba";
const prototypeFiles = ["colors.css", "spacing.css", "motion.css", "typography.css"];
const prototypeDarkCSS = fs.readFileSync(`${prototypeRoot}/tokens/dark.css`, "utf8");
const prototypeSpacingCSS = fs.readFileSync(`${prototypeRoot}/tokens/spacing.css`, "utf8");
const currentSource = fs.readFileSync(new URL("../../../internal/web/static/style.css", import.meta.url), "utf8");

function stripComments(value) {
  return value.replace(/\/\*[\s\S]*?\*\//g, "");
}

function declarations(value) {
  const result = {};
  for (const match of stripComments(value).matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) result[match[1]] = match[2].trim();
  return result;
}

function blockForSelector(source, selector) {
  const start = source.indexOf(selector);
  if (start < 0) return "";
  const open = source.indexOf("{", start);
  if (open < 0) return "";
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(open + 1, index);
    }
  }
  return "";
}

function compare(expected, actual) {
  const missing = [];
  const mismatched = [];
  for (const [name, expectedValue] of Object.entries(expected)) {
    if (!(name in actual)) {
      missing.push({ name, expected: expectedValue });
      continue;
    }
    if (actual[name] !== expectedValue) mismatched.push({ name, expected: expectedValue, actual: actual[name] });
  }
  return { missing, mismatched };
}

function merge(...maps) {
  return Object.assign({}, ...maps);
}

const expectedRoot = merge(...prototypeFiles.map((name) => declarations(blockForSelector(fs.readFileSync(`${prototypeRoot}/tokens/${name}`, "utf8"), ":root"))));
const expectedDarkOverrides = merge(declarations(blockForSelector(prototypeDarkCSS, ".dark")), declarations(blockForSelector(prototypeSpacingCSS, ".dark")));
expectedRoot["--sidebar-surface"] = "var(--sidebar)";
const currentRoot = declarations(blockForSelector(currentSource, ":root"));
const currentDarkOverrides = declarations(blockForSelector(currentSource, ".dark"));
const expectedDark = merge(expectedRoot, expectedDarkOverrides);
const currentDark = merge(currentRoot, currentDarkOverrides);
const report = {
  generatedAt: new Date().toISOString(),
  prototype: {
    rootFiles: prototypeFiles,
    darkFiles: ["dark.css", "spacing.css"]
  },
  light: compare(expectedRoot, currentRoot),
  dark: compare(expectedDark, currentDark),
  exact: false
};
report.exact = report.light.missing.length === 0 && report.light.mismatched.length === 0 && report.dark.missing.length === 0 && report.dark.mismatched.length === 0;
const output = new URL("./token-parity-audit.json", import.meta.url);
fs.writeFileSync(output, JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
