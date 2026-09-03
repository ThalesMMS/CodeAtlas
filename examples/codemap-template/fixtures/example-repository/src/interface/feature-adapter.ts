import { executeFeature, FeatureResult } from "../application/execute-feature";
import { FeatureCommand, ValidationError } from "../application/feature-command";

export const featureAdapter = {
  async execute(payload: unknown): Promise<FeatureResult> {
    try {
      const command = toFeatureCommand(payload);
      FeatureCommand.validate(command);
      return executeFeature.run(command);
    } catch (error) {
      if (error instanceof ValidationError) {
        return mapValidationError(error);
      }
      throw error;
    }
  },
};

export function toFeatureCommand(payload: unknown): FeatureCommand {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    throw new ValidationError("payload must be an object");
  }
  const value = payload as Partial<FeatureCommand>;
  return {
    id: String(value.id ?? ""),
    value: String(value.value ?? ""),
  };
}

export function mapValidationError(error: ValidationError): FeatureResult {
  return { id: "", status: "rejected", message: error.message };
}
