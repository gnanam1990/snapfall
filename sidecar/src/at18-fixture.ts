export const CIRCLE_TESTNET_FACILITATOR_ENDPOINTS = {
  verify: 'https://gateway-api-testnet.circle.com/gateway/v1/x402/verify',
  settle: 'https://gateway-api-testnet.circle.com/gateway/v1/x402/settle',
} as const;

export interface CircleV1Fixture {
  x402Version: 1;
  facilitatorEndpoints: {
    verify: string;
    settle: string;
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Assert the small, stable part of Vasanth's V1 fixture that AT-18 owns.
 *
 * The fixture may carry any additional request/response evidence. This validator
 * deliberately ignores it and only proves that the V1 exchange was configured
 * to use Circle's documented testnet verify and settle endpoints.
 */
export function validateCircleV1Fixture(fixture: unknown): CircleV1Fixture {
  if (!isRecord(fixture) || fixture.x402Version !== 1) {
    throw new Error('AT-18 requires an x402 V1 fixture');
  }

  const endpoints = fixture.facilitatorEndpoints;
  if (!isRecord(endpoints)) {
    throw new Error('AT-18 fixture is missing facilitatorEndpoints');
  }

  for (const operation of ['verify', 'settle'] as const) {
    const expected = CIRCLE_TESTNET_FACILITATOR_ENDPOINTS[operation];
    if (endpoints[operation] !== expected) {
      throw new Error(
        `AT-18 expected Circle ${operation} endpoint ${expected}; received ${String(endpoints[operation])}`,
      );
    }
  }

  return fixture as unknown as CircleV1Fixture;
}
