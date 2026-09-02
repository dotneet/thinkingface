import { describe, expect, it } from "vitest";
import {
  isExternalHref,
  makeMarkdownUrlTransform,
  markdownHrefTransform,
  markdownUrlTransform,
} from "@/lib/markdown-urls";

const ASSETS = "https://api.example.com/resolve/model/acme/bert/main/docs";
const ROOT = "https://api.example.com/resolve/model/acme/bert/main";

describe("markdownUrlTransform", () => {
  describe("without an asset base", () => {
    it("delegates to react-markdown's default sanitiser", () => {
      expect(markdownUrlTransform("./plot.png")).toBe("./plot.png");
      expect(markdownUrlTransform("https://example.com/a.png")).toBe("https://example.com/a.png");
      // The default transform drops unsafe protocols.
      expect(markdownUrlTransform("javascript:alert(1)")).toBe("");
    });
  });

  describe("with an asset base", () => {
    it("leaves in-page anchors alone", () => {
      expect(markdownUrlTransform("#usage", ASSETS)).toBe("#usage");
    });

    it("leaves protocol-relative and absolute URLs alone", () => {
      expect(markdownUrlTransform("//cdn.example.com/a.png", ASSETS)).toBe(
        "//cdn.example.com/a.png",
      );
      expect(markdownUrlTransform("https://example.com/a.png", ASSETS)).toBe(
        "https://example.com/a.png",
      );
      expect(markdownUrlTransform("mailto:a@example.com", ASSETS)).toBe("mailto:a@example.com");
    });

    it("still sanitises unsafe protocols", () => {
      expect(markdownUrlTransform("javascript:alert(1)", ASSETS)).toBe("");
    });

    it("resolves a bare relative path against the asset base", () => {
      expect(markdownUrlTransform("plot.png", ASSETS)).toBe(`${ASSETS}/plot.png`);
    });

    it("strips a single leading ./", () => {
      expect(markdownUrlTransform("./plot.png", ASSETS)).toBe(`${ASSETS}/plot.png`);
    });

    it("drops interior . segments", () => {
      expect(markdownUrlTransform("a/./b.png", ASSETS)).toBe(`${ASSETS}/a/b.png`);
    });

    it("keeps a query string or fragment attached to the asset", () => {
      // `plot.png?v=2` is a path plus a query, not one segment with an
      // encoded `?` in it (which the resolve endpoint would 404 on).
      expect(markdownUrlTransform("plot.png?v=2", ASSETS)).toBe(`${ASSETS}/plot.png?v=2`);
      expect(markdownUrlTransform("./img/a b.svg#icon", ASSETS)).toBe(
        `${ASSETS}/img/a%20b.svg#icon`,
      );
    });

    it("does not double up separators when the base already ends in one", () => {
      expect(markdownUrlTransform("plot.png", `${ASSETS}/`)).toBe(`${ASSETS}/plot.png`);
    });

    it("encodes each segment", () => {
      expect(markdownUrlTransform("my images/a b.png", ASSETS)).toBe(
        `${ASSETS}/my%20images/a%20b.png`,
      );
    });

    it("resolves a root-relative path against the repository root", () => {
      expect(markdownUrlTransform("/assets/a.png", ASSETS, ROOT)).toBe(`${ROOT}/assets/a.png`);
    });

    it("falls back to the asset base when no repository root is given", () => {
      expect(markdownUrlTransform("/assets/a.png", ASSETS)).toBe(`${ASSETS}/assets/a.png`);
    });

    it("collapses an empty relative path onto the base", () => {
      expect(markdownUrlTransform("", ASSETS)).toBe(`${ASSETS}/`);
    });
  });
});

describe("markdownUrlTransform — parent segments", () => {
  it("walks up out of the document's directory", () => {
    expect(markdownUrlTransform("../plot.png", ASSETS, ROOT)).toBe(`${ROOT}/plot.png`);
    expect(markdownUrlTransform("../img/plot.png", ASSETS, ROOT)).toBe(`${ROOT}/img/plot.png`);
  });

  it("clamps at the repository root instead of escaping it", () => {
    expect(markdownUrlTransform("../../../etc/passwd", ASSETS, ROOT)).toBe(`${ROOT}/etc/passwd`);
  });

  it("re-encodes a directory whose name was percent-escaped in the base", () => {
    expect(markdownUrlTransform("a.png", `${ROOT}/my%20docs`, ROOT)).toBe(
      `${ROOT}/my%20docs/a.png`,
    );
  });

  it("clamps a percent-encoded `..` too", () => {
    // `%2e%2e` used to slip past the literal ".." test and be pushed as an
    // ordinary segment, so the URL kept the `../` the browser then applied
    // itself — an `![img](%2e%2e/…)` fired a GET outside the repository
    // prefix with no click involved.
    expect(markdownUrlTransform("%2e%2e/%2e%2e/%2e%2e/secret.png", ASSETS, ROOT)).toBe(
      `${ROOT}/secret.png`,
    );
    expect(markdownUrlTransform("%2E%2E/plot.png", ASSETS, ROOT)).toBe(`${ROOT}/plot.png`);
    // A percent-encoded separator must not smuggle a `..` through either.
    expect(markdownUrlTransform("%2f..%2f..%2fsecret.png", ASSETS, ROOT)).toBe(
      `${ROOT}/secret.png`,
    );
  });
});

describe("markdownHrefTransform", () => {
  const CTX = {
    kind: "model" as const,
    ns: "acme",
    name: "bert",
    rev: "main",
    dir: ["docs"],
  };

  it("leaves links alone without a link context", () => {
    expect(markdownHrefTransform("usage.md")).toBe("usage.md");
    // Notably it does *not* become a resolve URL: that turned a link to
    // another page into a raw-text download.
    expect(markdownHrefTransform("usage.md", undefined)).toBe("usage.md");
  });

  it("turns a relative link into a blob page in the same directory", () => {
    expect(markdownHrefTransform("usage.md", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/usage.md",
    );
    expect(markdownHrefTransform("./usage.md", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/usage.md",
    );
  });

  it("resolves a root-relative link against the repository root", () => {
    expect(markdownHrefTransform("/CONTRIBUTING.md", CTX)).toBe(
      "/models/acme/bert/blob/main/CONTRIBUTING.md",
    );
  });

  it("normalises parent segments", () => {
    expect(markdownHrefTransform("../LICENSE", CTX)).toBe("/models/acme/bert/blob/main/LICENSE");
    expect(markdownHrefTransform("../guides/./a.md", CTX)).toBe(
      "/models/acme/bert/blob/main/guides/a.md",
    );
  });

  it("normalises a percent-encoded parent segment before it reaches the browser", () => {
    // `[go](%2e%2e/…/settings/tokens)` used to produce a href the app believed
    // was a repo-relative blob link and the browser resolved to /settings/tokens.
    expect(markdownHrefTransform("%2e%2e/%2e%2e/%2e%2e/settings/tokens", CTX)).toBe(
      "/models/acme/bert/blob/main/settings/tokens",
    );
    expect(markdownHrefTransform("%2e%2e/LICENSE", CTX)).toBe(
      "/models/acme/bert/blob/main/LICENSE",
    );
    expect(markdownHrefTransform("%2f..%2f..%2fLICENSE", CTX)).toBe(
      "/models/acme/bert/blob/main/LICENSE",
    );
    // Whatever comes out, it never carries a `..` for the browser to apply.
    for (const input of [
      "%2e%2e/%2e%2e/x.md",
      "%2E%2E/%2E%2E/%2E%2E/x.md",
      "docs/%2e%2e/%2e%2e/x.md",
    ]) {
      expect(markdownHrefTransform(input, CTX), input).not.toContain("..");
    }
  });

  it("sends a trailing slash to the tree page", () => {
    expect(markdownHrefTransform("examples/", CTX)).toBe(
      "/models/acme/bert/tree/main/docs/examples",
    );
    expect(markdownHrefTransform("/", CTX)).toBe("/models/acme/bert/tree/main");
  });

  it("keeps a query string attached", () => {
    expect(markdownHrefTransform("usage.md?plain=1", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/usage.md?plain=1",
    );
  });

  it("re-points in-page anchors at the namespaced heading ids", () => {
    // Headings get `user-content-` ids (see HEADING_ID_PREFIX), with or
    // without a link context; footnote links are already namespaced.
    expect(markdownHrefTransform("#install", CTX)).toBe("#user-content-install");
    expect(markdownHrefTransform("#install")).toBe("#user-content-install");
    expect(markdownHrefTransform("#user-content-fn-1", CTX)).toBe("#user-content-fn-1");
    // …and the footnotes heading keeps its fixed, unprefixed id.
    expect(markdownHrefTransform("#footnote-label", CTX)).toBe("#footnote-label");
    expect(markdownHrefTransform("#", CTX)).toBe("#");
  });

  it("namespaces the fragment of a cross-file link too", () => {
    expect(markdownHrefTransform("usage.md#install", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/usage.md#user-content-install",
    );
    expect(markdownHrefTransform("usage.md?x=1#install", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/usage.md?x=1#user-content-install",
    );
  });

  it("leaves absolute URLs and mailto alone", () => {
    expect(markdownHrefTransform("https://example.com/x", CTX)).toBe("https://example.com/x");
    expect(markdownHrefTransform("//cdn.example.com/x", CTX)).toBe("//cdn.example.com/x");
    expect(markdownHrefTransform("mailto:a@example.com", CTX)).toBe("mailto:a@example.com");
  });

  it("still sanitises unsafe protocols", () => {
    expect(markdownHrefTransform("javascript:alert(1)", CTX)).toBe("");
  });

  it("re-encodes a percent-escaped file name once", () => {
    expect(markdownHrefTransform("my%20guide.md", CTX)).toBe(
      "/models/acme/bert/blob/main/docs/my%20guide.md",
    );
  });

  it("uses the repository kind in the route", () => {
    expect(markdownHrefTransform("a.md", { ...CTX, kind: "dataset", dir: [] })).toBe(
      "/datasets/acme/bert/blob/main/a.md",
    );
  });
});

describe("makeMarkdownUrlTransform", () => {
  const ctx = { kind: "model" as const, ns: "acme", name: "bert", rev: "main", dir: [] };

  it("routes src through the resolve endpoint and href through the app", () => {
    const transform = makeMarkdownUrlTransform({
      assetBaseUrl: ROOT,
      repoRootUrl: ROOT,
      linkContext: ctx,
    });
    expect(transform("plot.png", "src")).toBe(`${ROOT}/plot.png`);
    expect(transform("poster.png", "poster")).toBe(`${ROOT}/poster.png`);
    expect(transform("usage.md", "href")).toBe("/models/acme/bert/blob/main/usage.md");
  });
});

describe("isExternalHref", () => {
  it("recognises absolute and protocol-relative URLs", () => {
    expect(isExternalHref("https://example.com")).toBe(true);
    expect(isExternalHref("http://example.com")).toBe(true);
    expect(isExternalHref("//example.com")).toBe(true);
  });

  it("treats in-app routes, anchors and mailto as internal", () => {
    expect(isExternalHref("/models/acme/bert")).toBe(false);
    expect(isExternalHref("#usage")).toBe(false);
    expect(isExternalHref("mailto:a@example.com")).toBe(false);
    expect(isExternalHref(undefined)).toBe(false);
  });
});
