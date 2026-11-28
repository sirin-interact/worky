import assert from "node:assert/strict";
import { test } from "node:test";

import { baseName, dirName, isWithin, join, parent, relative, samePath, tilde } from "./paths.ts";

test("a path is within a root on its own terms, not this machine's", () => {
  assert.ok(isWithin("C:\\src\\app\\src\\a.ts", "C:\\src\\app", "windows"));
  assert.ok(isWithin("c:/SRC/app", "C:\\src\\app\\", "windows"));
  assert.ok(!isWithin("C:\\src\\app-fix\\a.ts", "C:\\src\\app", "windows"));
  assert.ok(isWithin("/src/app/a.ts", "/src/app", "linux"));
  assert.ok(!isWithin("/src/App/a.ts", "/src/app", "linux"));
  assert.ok(isWithin("/Users/me/App/a.ts", "/Users/me/app", "darwin"));
  assert.ok(isWithin("/", "/", "linux"));
  assert.ok(isWithin("/src", "/", "linux"));
});

test("same path ignores case and trailing separators where the OS does", () => {
  assert.ok(samePath("C:\\Src\\App\\", "c:\\src\\app", "windows"));
  assert.ok(!samePath("/src/App", "/src/app", "linux"));
});

test("names and folders come apart at either separator", () => {
  assert.equal(baseName("C:\\src\\app\\"), "app");
  assert.equal(baseName("/src/app/a.ts"), "a.ts");
  assert.equal(dirName("src/ui/tree.ts"), "src/ui");
  assert.equal(dirName("README.md"), "");
});

test("join uses the host's separator", () => {
  assert.equal(join("C:\\src\\app", "src/ui/a.ts", "windows"), "C:\\src\\app\\src\\ui\\a.ts");
  assert.equal(join("C:\\", "src", "windows"), "C:\\src");
  assert.equal(join("/src/app", "src/ui/a.ts", "linux"), "/src/app/src/ui/a.ts");
  assert.equal(join("/src/app", "", "linux"), "/src/app");
});

test("parent stops at the root", () => {
  assert.equal(parent("C:\\src\\app", "windows"), "C:\\src");
  assert.equal(parent("C:\\src", "windows"), "C:\\");
  assert.equal(parent("C:\\", "windows"), null);
  assert.equal(parent("/src/app", "linux"), "/src");
  assert.equal(parent("/src", "linux"), "/");
  assert.equal(parent("/", "linux"), null);
});

test("relative paths use forward slashes", () => {
  assert.equal(relative("C:\\src\\app\\src\\ui\\a.ts", "C:\\src\\app", "windows"), "src/ui/a.ts");
  assert.equal(relative("C:\\src\\app", "C:\\src\\app", "windows"), "");
  assert.equal(relative("/elsewhere/a.ts", "/src/app", "linux"), "/elsewhere/a.ts");
});

test("the home directory is shortened for display", () => {
  assert.equal(tilde("C:\\Users\\me\\worky\\app", "C:\\Users\\me", "windows"), "~/worky/app");
  assert.equal(tilde("/srv/app", "/home/me", "linux"), "/srv/app");
  assert.equal(tilde("/srv/app", "", "linux"), "/srv/app");
});
