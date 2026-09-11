# READ-ONLY. Replicates the portal query the report server runs when building a
# student-actions report (report-service/server/lib/report_server/reports/athena/
# learner_data.ex `fetch/3`), so we can see the learner population a run would
# inline as secure keys *before* submitting the run.
#
# The report server selects one row per learner and then inlines every distinct
# secure key into the Athena SQL, so the run's size is driven by the learner
# count across ALL classes that ever ran the assignment -- not by the classes we
# care about. This script counts that population, broken down by assignment and
# class, so we can pick a filter that fits under the 256 KB SQL ceiling.
#
# Run from the portal app container:
#   sudo -n docker exec -i <container> bundle exec rails runner - < learner_census.rb

ACTIVITY_IDS = [2460, 2462, 2463, 2464, 2465, 2467, 2468, 3052, 3053,
                2735, 2739, 3255, 3529, 3530, 2841].freeze

# Same FROM/JOIN chain as LearnerData.fetch. No project scoping: a super-admin
# gets :all, which applies none.
FROM = <<~SQL
  FROM report_learners rl
  JOIN portal_learners pl ON (rl.learner_id = pl.id)
  JOIN users u ON (u.id = rl.user_id)
  JOIN portal_offerings po ON (po.id = rl.offering_id)
  JOIN external_activities ea ON (po.runnable_type = 'ExternalActivity' AND po.runnable_id = ea.id)
  JOIN portal_student_clazzes psc ON (psc.student_id = rl.student_id)
  JOIN portal_teacher_clazzes ptc ON (ptc.clazz_id = psc.clazz_id AND rl.class_id = ptc.clazz_id)
  JOIN portal_clazzes pc ON (pc.id = rl.class_id)
  WHERE po.runnable_id IN (#{ACTIVITY_IDS.join(',')})
SQL

conn = ActiveRecord::Base.connection

rows = conn.select_all(<<~SQL).to_a
  SELECT po.runnable_id AS activity_id,
         ea.name        AS activity_name,
         rl.class_id,
         rl.class_name,
         rl.school_id,
         rl.school_name,
         pc.class_hash,
         GROUP_CONCAT(DISTINCT ptc.teacher_id) AS teacher_ids,
         COUNT(DISTINCT rl.learner_id) AS learners
  #{FROM}
  GROUP BY po.runnable_id, ea.name, rl.class_id, rl.class_name,
           rl.school_id, rl.school_name, pc.class_hash
SQL

require 'csv'
puts CSV.generate_line(%w[activity_id activity_name class_id class_name school_id
                          school_name class_hash teacher_ids learners]).chomp
rows.each do |r|
  puts CSV.generate_line([r['activity_id'], r['activity_name'], r['class_id'], r['class_name'],
                          r['school_id'], r['school_name'], r['class_hash'],
                          r['teacher_ids'], r['learners']]).chomp
end

warn "rows: #{rows.size}"
warn "distinct learners across all 15: " \
     "#{conn.select_value("SELECT COUNT(DISTINCT rl.learner_id) #{FROM}")}"
