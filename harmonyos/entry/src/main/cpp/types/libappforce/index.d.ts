export const available: () => boolean;
export const call: (request: string) => Promise<string>;
export const start: (directory: string, tunFd: number,
  protect: (socketFd: number) => Promise<void>) => Promise<string>;
export const stop: () => Promise<string>;
