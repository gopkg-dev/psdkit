export interface BinaryOutput {
  id: string; name: string; mime: string; length: number; data: ArrayBuffer;
}
export interface Reply<T = unknown> { ok: true; data: T; binaries?: BinaryOutput[]; }
export interface CallOptions {
  target?: string; args?: unknown[]; links?: Record<string, string>;
  name?: string; kind?: string; alias?: string; timeoutMS?: number;
  /** Request lossless PNG without the duplicate raw RGBA transfer. Defaults to both. */
  imageOutput?: 'png';
  /** Ownership of a full ArrayBuffer is transferred to the Worker. Subviews are copied. */
  bytes?: ArrayBuffer | ArrayBufferView;
}
export interface Ready { ready: true; version: string; }
export class PsdkitClient {
  constructor(options?: { workerURL?: string | URL; timeoutMS?: number });
  ready(): Promise<Ready>;
  call<T = unknown>(operation: string, options?: CallOptions): Promise<Reply<T>>;
  importFile(file: File, kind: string): Promise<Reply<{ asset: string; name: string; kind: string; bytes: number }>>;
  reset(): Promise<Ready>;
  terminate(error?: Error): void;
  close(): void;
}
export function binary(reply: Reply, id: string): BinaryOutput | undefined;
export function download(item: BinaryOutput, name?: string): void;
