# Tool catalog and disclosure budget mismatch

## Discovery

A source audit on 2026-10-24 found two independent boundary failures. No production outage was observed. MCP accepted a server catalog of up to 4096 tools and individual schemas up to 256 KiB, but Code Mode reused its 512-call execution limit as a catalog-size limit and embedded the complete schema in each description capped at 64 KiB. A valid remote tool could therefore prevent an unrelated `exec` from starting, including when its declaration was deferred.

The search result budget separately reserved approximately 10 KiB for complete tool declarations. A larger declaration was always omitted, without a bounded path to retrieve it. Treating declaration visibility as separate from permission was correct, but the discovery flow could not finish.

## Correction

Catalog size now has an independent limit. Code Mode prepares bounded short metadata once per occupy and refers to direct declarations rather than copying full schemas into its description or VM metadata. The host registers that metadata catalog with the worker once, then uses a digest reference; the existing occupy termination barrier retires registered catalogs. Host-side input validation and callback capability checks remain authoritative.

Search exposes large declarations through digest-bound continuous UTF-8 pages. History reconstruction proves completion from paired, successful, actually delivered result content, not from auxiliary Details. Every fragment must match the current immutable declaration at its requested offset. A lone final page, an old digest, missing fragments, or rewritten content cannot activate the declaration.

MCP managers retain bounded search-index projections keyed by explicit immutable tool identities. Tool replacements, active-tool changes and reconnects affect the next occupy; existing occupies retain their original snapshot. Model-visible projections and Code Mode metadata are reused without bypassing transformed-history disclosure checks.

## Regression coverage

Tests cover 4096 tools with a one-call execution budget, large schemas without duplicate exec declarations, worker registration reuse and retirement, complete 256 KiB pages with Chinese and escaped text, forged and missing history fragments, cache eviction and snapshot isolation, and portable server-side promotion after the final page. Race tests cover the changed runtime, discovery and MCP packages.
