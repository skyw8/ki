// @ts-nocheck
// TTL cooldown registry for free models.
//
// Two failure classes get different cooldowns:
// - exhausted (429/5xx/400/422): long TTL, aligned with OpenRouter's
//   per-minute quota reset window.
// - slow (first-token timeout): short TTL — the model is alive but slow, so
//   other candidates get a chance but it recovers into the pool quickly.
const DEFAULT_EXHAUSTION_TTL_MS = 90_000;
const DEFAULT_SLOW_TTL_MS = 15_000;
export class FreeRouter {
    /**
     * @param {readonly string[]} models ordered candidate list (preference order)
     * @param {{exhaustedTtlMs?: number, slowTtlMs?: number, now?: () => number}} options
     */
    constructor(models, options = {}) {
        this.models = [...models];
        this.exhaustedTtlMs = options.exhaustedTtlMs ?? DEFAULT_EXHAUSTION_TTL_MS;
        this.slowTtlMs = options.slowTtlMs ?? DEFAULT_SLOW_TTL_MS;
        this.now = options.now ?? Date.now;
        /** @type {Map<string, {at: number, ttl: number}>} */
        this.exhausted = new Map();
    }
    /** Available model ids in preference order, skipping active cooldowns. */
    nextModels(count = this.models.length) {
        const now = this.now();
        const result = [];
        for (const id of this.models) {
            if (result.length >= count)
                break;
            const entry = this.exhausted.get(id);
            if (entry !== undefined) {
                if (now - entry.at < entry.ttl)
                    continue;
                this.exhausted.delete(id); // TTL expired — back in rotation
            }
            result.push(id);
        }
        return result;
    }
    /** Quota-exhausted or request-rejected: long cooldown. */
    markExhausted(id) {
        if (!this.models.includes(id))
            return;
        this.exhausted.set(id, { at: this.now(), ttl: this.exhaustedTtlMs });
    }
    /** First-token timeout: short cooldown. Never downgrades a long one. */
    markSlow(id) {
        if (!this.models.includes(id))
            return;
        const existing = this.exhausted.get(id);
        if (existing && existing.ttl >= this.exhaustedTtlMs)
            return;
        this.exhausted.set(id, { at: this.now(), ttl: this.slowTtlMs });
    }
}
