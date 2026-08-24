import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  canCreateInNamespace,
  canEditNamespace,
  getNamespace,
  isReservedNamespace,
  namespaceHref,
  namespaceTabHref,
  parseNamespaceTab,
  updateMyProfile,
  writableNamespaces,
} from "@/lib/namespace";
import type { MembersVisibility, NamespaceProfile, OrgRole, User } from "@/types/api";

type Call = { url: string; method: string; body?: string };

function mockFetch(status = 200, body: unknown = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({
        url,
        method: init?.method ?? "GET",
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

function profile(overrides: Partial<NamespaceProfile> = {}): NamespaceProfile {
  return {
    name: "alice",
    kind: "user",
    display_name: "",
    description: "",
    website: "",
    avatar_url: "",
    created_at: "2026-01-01T00:00:00Z",
    num_models: 0,
    num_datasets: 0,
    num_experiments: 0,
    num_members: 0,
    // tygo renders both as the union of their named constants only, so the
    // "no value" case the backend really sends has to be cast in.
    members_visibility: "" as MembersVisibility,
    viewer_role: "" as OrgRole,
    can_edit: false,
    ...overrides,
  };
}

beforeEach(() => {
  vi.stubEnv("API_URL", "http://localhost:8080");
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("namespaceHref", () => {
  it("prefixes the namespace with a slash", () => {
    expect(namespaceHref("alice")).toBe("/alice");
  });

  it("percent-encodes characters that would otherwise split the path", () => {
    expect(namespaceHref("a b")).toBe("/a%20b");
    expect(namespaceHref("a/b")).toBe("/a%2Fb");
  });
});

describe("namespaceTabHref", () => {
  it("omits the query for the default 'models' tab", () => {
    expect(namespaceTabHref("alice", "models")).toBe("/alice");
  });

  it("appends ?tab= for every other tab", () => {
    expect(namespaceTabHref("alice", "datasets")).toBe("/alice?tab=datasets");
    expect(namespaceTabHref("alice", "experiments")).toBe("/alice?tab=experiments");
    expect(namespaceTabHref("acme", "members")).toBe("/acme?tab=members");
  });

  it("encodes the namespace segment the same way namespaceHref does", () => {
    expect(namespaceTabHref("a b", "datasets")).toBe("/a%20b?tab=datasets");
  });
});

describe("parseNamespaceTab", () => {
  it("recognises datasets and experiments regardless of kind", () => {
    expect(parseNamespaceTab("datasets")).toBe("datasets");
    expect(parseNamespaceTab("experiments", "user")).toBe("experiments");
    expect(parseNamespaceTab("datasets", "org")).toBe("datasets");
  });

  it("falls back to models for undefined or unrecognised values", () => {
    expect(parseNamespaceTab(undefined)).toBe("models");
    expect(parseNamespaceTab("")).toBe("models");
    expect(parseNamespaceTab("bogus")).toBe("models");
    expect(parseNamespaceTab("Models")).toBe("models"); // case-sensitive: not a recognised value either
  });

  it("only accepts 'members' for an organisation namespace", () => {
    expect(parseNamespaceTab("members", "org")).toBe("members");
    expect(parseNamespaceTab("members", "user")).toBe("models");
    expect(parseNamespaceTab("members")).toBe("models"); // no kind given: cannot be an org
  });
});

describe("isReservedNamespace", () => {
  it("flags a name from the reserved list", () => {
    expect(isReservedNamespace("settings")).toBe(true);
    expect(isReservedNamespace("orgs")).toBe(true);
  });

  it("is case-insensitive", () => {
    expect(isReservedNamespace("Settings")).toBe(true);
    expect(isReservedNamespace("SETTINGS")).toBe(true);
    expect(isReservedNamespace("SeTtInGs")).toBe(true);
  });

  it("does not flag an ordinary namespace name", () => {
    expect(isReservedNamespace("alice")).toBe(false);
    expect(isReservedNamespace("acme-research")).toBe(false);
  });
});

describe("canEditNamespace", () => {
  it("mirrors the server's can_edit flag", () => {
    expect(canEditNamespace(profile({ can_edit: true }))).toBe(true);
    expect(canEditNamespace(profile({ can_edit: false }))).toBe(false);
  });
});

describe("canCreateInNamespace", () => {
  const me = (namespaces: User["namespaces"]): User => ({
    id: 1,
    username: "alice",
    email: "",
    is_admin: false,
    display_name: "",
    avatar_url: "",
    namespaces,
  });

  it("is true when the viewer's /me lists the namespace with admin or write", () => {
    const p = profile({ name: "acme", kind: "org" });
    expect(canCreateInNamespace(p, me([{ name: "acme", kind: "org", role: "admin" }]))).toBe(true);
    expect(canCreateInNamespace(p, me([{ name: "acme", kind: "org", role: "write" }]))).toBe(true);
    // Namespace names are case-insensitive.
    expect(canCreateInNamespace(p, me([{ name: "ACME", kind: "org", role: "write" }]))).toBe(true);
  });

  it("is false for a read member, a namespace missing from /me, or no viewer", () => {
    const p = profile({ name: "acme", kind: "org" });
    expect(canCreateInNamespace(p, me([{ name: "acme", kind: "org", role: "read" }]))).toBe(false);
    expect(canCreateInNamespace(p, me([{ name: "other", kind: "org", role: "admin" }]))).toBe(
      false,
    );
    expect(canCreateInNamespace(p, null)).toBe(false);
  });

  it("ignores viewer_role / can_edit: a site admin is admin everywhere but /new cannot target another user's namespace", () => {
    const p = profile({ name: "bob", kind: "user", can_edit: true, viewer_role: "admin" });
    expect(canCreateInNamespace(p, me([{ name: "alice", kind: "user", role: "admin" }]))).toBe(
      false,
    );
  });
});

describe("writableNamespaces", () => {
  const me = (namespaces: User["namespaces"]): User => ({
    id: 1,
    username: "alice",
    email: "",
    is_admin: false,
    display_name: "",
    avatar_url: "",
    namespaces,
  });

  it("keeps admin and write roles, for both personal and org namespaces", () => {
    const user = me([
      { name: "alice", kind: "user", role: "admin" }, // owner of their own namespace
      { name: "acme", kind: "org", role: "write" },
    ]);
    expect(writableNamespaces(user).map((n) => n.name)).toEqual(["alice", "acme"]);
  });

  it("drops a read membership", () => {
    const user = me([
      { name: "acme", kind: "org", role: "read" },
      { name: "other-org", kind: "org", role: "write" },
    ]);
    expect(writableNamespaces(user).map((n) => n.name)).toEqual(["other-org"]);
  });

  it("returns an empty list when every membership is read-only", () => {
    const user = me([{ name: "acme", kind: "org", role: "read" }]);
    expect(writableNamespaces(user)).toEqual([]);
  });

  it("returns an empty list for a user with no namespaces at all", () => {
    expect(writableNamespaces(me([]))).toEqual([]);
  });
});

describe("endpoints", () => {
  it("GETs the namespace by percent-encoded name", async () => {
    const calls = mockFetch(200, { namespace: profile() });
    await getNamespace("a b");
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/namespaces/a%20b");
    expect(calls[0]?.method).toBe("GET");
  });

  it("PATCHes the profile update to /api/v1/me/profile", async () => {
    const calls = mockFetch(200, { namespace: profile() });
    await updateMyProfile({ display_name: "Alice", website: "https://example.com" });
    expect(calls[0]?.url).toBe("http://localhost:8080/api/v1/me/profile");
    expect(calls[0]?.method).toBe("PATCH");
    expect(calls[0]?.body).toBe(
      JSON.stringify({ display_name: "Alice", website: "https://example.com" }),
    );
  });

  it("never throws when the backend is unreachable (CLAUDE.md invariant 3)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("ECONNREFUSED");
      }),
    );
    const result = await getNamespace("alice");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.status).toBe(0);
  });

  it("surfaces a 404 for a namespace nobody holds", async () => {
    mockFetch(404, { error: { type: "not_found", message: "no such namespace" } });
    const result = await getNamespace("nobody");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.status).toBe(404);
  });
});
