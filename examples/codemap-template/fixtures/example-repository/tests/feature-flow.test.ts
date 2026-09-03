import { executeFeature } from "../src/application/execute-feature";
import { featureAdapter } from "../src/interface/feature-adapter";
import {
  eventPublisher,
  featureRepository,
  operationLog,
  outbox,
  unitOfWork,
} from "../src/infrastructure/dependencies";

describe("feature delivery", () => {
  beforeEach(() => {
    operationLog.length = 0;
    featureRepository.savedIDs.length = 0;
    unitOfWork.commits = 0;
    eventPublisher.publishedTypes.length = 0;
    outbox.stagedTypes.length = 0;
    outbox.deliveredTypes.length = 0;
  });

  it("persists the aggregate before publishing the domain event", async () => {
    const result = await executeFeature.run({ id: "feature-1", value: "new-value" });

    expect(result.status).toBe("accepted");
    expect(featureRepository.savedIDs).toContain("feature-1");
    expect(unitOfWork.commits).toBe(1);
    expect(eventPublisher.publishedTypes).toContain("FeatureChanged");
    expect(outbox.deliveredTypes).toContain("FeatureChanged");
    expect(operationLog).toEqual(["save", "stage", "commit", "publish", "mark-delivered"]);
  });

  it("maps nullish payloads to rejected validation results", async () => {
    await expect(featureAdapter.execute(null)).resolves.toMatchObject({ status: "rejected" });
    await expect(featureAdapter.execute(undefined)).resolves.toMatchObject({ status: "rejected" });
  });
});
