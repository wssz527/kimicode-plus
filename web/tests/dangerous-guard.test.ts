import test from "node:test";
import assert from "node:assert/strict";
import { dangerousGuardPatch, readDangerousGuard } from "../src/native.js";

const rules = [
  { decision: "ask", scope: "user", pattern: "read", reason: "读取项目文件前询问" },
];

test("the guard reads back from either spelling and defaults to on", () => {
  assert.equal(readDangerousGuard({ permission: { dangerousCommandGuard: false } }), false);
  assert.equal(readDangerousGuard({ permission: { dangerous_command_guard: false } }), false);
  assert.equal(readDangerousGuard({ permission: { dangerousCommandGuard: true } }), true);
  assert.equal(readDangerousGuard({ permission: {} }), true);
  assert.equal(readDangerousGuard({}), true);
  assert.equal(readDangerousGuard(null), true);
});

test("the guard patch carries only the guard field", () => {
  // POST /config merges a section key by key, so a patch without `rules` leaves
  // the stored rules alone. Adding them here would rewrite whatever is on the
  // server with this form's copy.
  assert.deepEqual(dangerousGuardPatch(false), { permission: { dangerous_command_guard: false } });
  assert.deepEqual(dangerousGuardPatch(true), { permission: { dangerous_command_guard: true } });
  assert.equal("rules" in (dangerousGuardPatch(false).permission as Record<string, unknown>), false);
});

test("saving the guard keeps the server rules and verifies the value read back", async () => {
  // Stub core that merges like POST /config does: it converts the snake_case
  // patch to camelCase and updates only the keys it names.
  let stored: Record<string, unknown> = { permission: { rules, dangerousCommandGuard: true } };
  const calls: { action: string; payload?: Record<string, unknown> }[] = [];
  const toCamel = (section: Record<string, unknown>) => Object.fromEntries(
    Object.entries(section).map(([key, value]) => [key.replace(/_([a-z])/g, (_, letter: string) => letter.toUpperCase()), value]),
  );
  const host = {
    request: async <T = unknown>(action: string, payload?: Record<string, unknown>): Promise<T> => {
      calls.push({ action, payload });
      if (action === "config.save") {
        const patch = (payload?.patch ?? {}) as Record<string, Record<string, unknown>>;
        const next: Record<string, unknown> = { ...stored };
        for (const [section, value] of Object.entries(patch)) {
          next[section] = { ...(stored[section] as Record<string, unknown> | undefined), ...toCamel(value) };
        }
        stored = next;
        return {} as T;
      }
      if (action === "config.get") return stored as T;
      throw new Error(`unexpected action ${action}`);
    },
  };

  // Mirrors saveGuard in NativeSettings: write the guard, read back, compare.
  async function saveGuard(enabled: boolean) {
    await host.request("config.save", { patch: dangerousGuardPatch(enabled) });
    return readDangerousGuard(await host.request("config.get"));
  }

  assert.equal(await saveGuard(false), false);
  assert.deepEqual(calls.map(call => call.action), ["config.save", "config.get"]);
  assert.deepEqual(calls[0]!.payload, { patch: { permission: { dangerous_command_guard: false } } });
  // The uncommitted rules in the form are still the stored ones.
  const permission = stored.permission as Record<string, unknown>;
  assert.deepEqual(permission.rules, rules);
  assert.equal(permission.dangerousCommandGuard, false);
});

test("an accepted write that the core ignored is reported by the read back", async () => {
  // A 200 from config.save proves acceptance, not that the field changed; the
  // schema drops unknown keys silently. The read back is what decides.
  const host = {
    request: async <T = unknown>(action: string, _payload?: Record<string, unknown>): Promise<T> => {
      if (action === "config.save") return {} as T;
      return { permission: { rules, dangerousCommandGuard: true } } as T;
    },
  };
  await host.request("config.save", { patch: dangerousGuardPatch(false) });
  assert.notEqual(readDangerousGuard(await host.request("config.get")), false);
});
