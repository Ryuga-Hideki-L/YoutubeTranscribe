import { TestBed } from '@angular/core/testing';
import { Api, ApiError } from './api';

describe('Api', () => {
  const realFetch = globalThis.fetch;
  const realEventSource = globalThis.EventSource;

  beforeEach(() => {
    globalThis.EventSource = class {
      addEventListener() {}
      close() {}
    } as unknown as typeof EventSource;
  });

  afterEach(() => {
    globalThis.fetch = realFetch;
    globalThis.EventSource = realEventSource;
  });

  // #20: пустой state стирал список каналов вместо ошибки
  it('бросает bad_response на ответ 200 с телом не-JSON', async () => {
    globalThis.fetch = (async () => new Response('<html>', { status: 200 })) as typeof fetch;
    const api = TestBed.inject(Api);

    const failure = await api.add('UC1', 200).catch((err: unknown) => err);

    expect(failure).toBeInstanceOf(ApiError);
    expect((failure as ApiError).code).toBe('bad_response');
    expect(api.state()).toBeNull();
  });

  it('берёт код и текст ошибки из тела не-2xx', async () => {
    const body = JSON.stringify({ code: 'quota', error: 'Квота исчерпана' });
    globalThis.fetch = (async () => new Response(body, { status: 403 })) as typeof fetch;
    const api = TestBed.inject(Api);

    const failure = await api.resolve('x').catch((err: unknown) => err);

    expect((failure as ApiError).code).toBe('quota');
    expect((failure as ApiError).message).toBe('Квота исчерпана');
  });
});
