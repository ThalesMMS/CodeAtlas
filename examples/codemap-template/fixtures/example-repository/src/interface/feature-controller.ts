import { featureAdapter } from "./feature-adapter";

export interface RequestLike {
  json(): Promise<unknown>;
}

export interface ResponseLike {
  status: number;
  body: unknown;
}

export async function handleFeatureRequest(request: RequestLike): Promise<ResponseLike> {
  const payload = await request.json();

  return featureAdapter.execute(payload);
}
