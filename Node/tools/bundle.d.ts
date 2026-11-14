export interface BundleOptions {
    project: string;
    runtime: 'nodejs';
    entry?: string;
    workers?: string[];
    assets?: string[];
    outdir?: string;
    debug?: boolean;
    force?: boolean;
    assetRoot?: string;
    sdkRoot?: string;
}
export interface BundleReport {
    version: 1;
    cache_hit: boolean;
    fingerprint: string;
    settings: Record<string, unknown>;
    input_sha256: Record<string, string | null>;
    output_sha256: Record<string, string>;
    outputs: Record<string, { bytes: number; gzip_bytes: number }>;
    metafile: import('esbuild').Metafile;
}
// Build a self-contained plugin bundle and return its size and provenance report
export function bundlePlugin(options: BundleOptions): Promise<BundleReport>;
