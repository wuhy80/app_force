import { http } from '@kit.NetworkKit';

export async function downloadSubscription(url: string): Promise<string> {
  if (!/^https:\/\/[^\s]+$/i.test(url)) { throw new Error('订阅地址必须以 https:// 开头'); }
  const request = http.createHttp();
  try {
    const response = await request.request(url, {
      method: http.RequestMethod.GET,
      expectDataType: http.HttpDataType.STRING,
      connectTimeout: 15000,
      readTimeout: 30000,
      maxLimit: 8 * 1024 * 1024,
      usingCache: false
    });
    if (response.responseCode !== 200) { throw new Error(`订阅下载失败（HTTP ${response.responseCode}）`); }
    const content = response.result;
    if (typeof content !== 'string' || content.length === 0) { throw new Error('订阅内容为空'); }
    if (content.length > 8 * 1024 * 1024) { throw new Error('订阅超过 8 MiB'); }
    return content;
  } finally {
    request.destroy();
  }
}
